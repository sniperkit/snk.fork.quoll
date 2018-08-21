/*
Sniperkit-Bot
- Status: analyzed
*/

package evtstore

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path"
	"time"

	"github.com/blang/vfs"
	"github.com/v2pro/plz/countlog"

	"github.com/sniperkit/snk.fork.quoll/discr"
	"github.com/sniperkit/snk.fork.quoll/lz4"
	"github.com/sniperkit/snk.fork.quoll/timeutil"
)

const fileHeaderSize = 7
const blockHeaderSize = 18
const blockIdSize = 20
const entryHeaderSize = 8
const filenamePattern = "200601021504"

var CST *time.Location

func init() {
	var err error
	CST, err = time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic("timezone Asia/Shanghai not loaded: " + err.Error())
	}
}

type Config struct {
	BlockEntriesCountLimit uint16
	BlockSizeLimit         int
	MaximumFlushInterval   time.Duration
	KeepFilesCount         int
}

var defaultConfig = Config{
	BlockEntriesCountLimit: math.MaxUint16 - 1,
	BlockSizeLimit:         1024 * 1024, // byte
	MaximumFlushInterval:   1 * time.Second,
	KeepFilesCount:         24,
}

type evtInput struct {
	eventTS   time.Time
	eventBody discr.EventBody
}

type EventEntry []byte // size(4byte)|timestamp(4byte)|body

func (entry EventEntry) EventCTS() uint32 {
	return binary.LittleEndian.Uint32(entry[4:])
}
func (entry EventEntry) EventBody() discr.EventBody {
	return discr.EventBody(entry[8:])
}

type CompressedEventEntries []byte
type EventEntries []byte

func (entries EventEntries) Next() (EventEntry, EventEntries) {
	if len(entries) < entryHeaderSize {
		panic("no more entry")
	}
	size := binary.LittleEndian.Uint32(entries)
	return EventEntry(entries[:size+entryHeaderSize]), entries[size+entryHeaderSize:]
}

type EventBlocks []byte // EventBlockId|EventBlock|EventBlockId|EventBlock|...

func (blocks EventBlocks) Next() (EventBlockId, EventBlock, EventBlocks) {
	if len(blocks) < blockIdSize {
		panic("no more block")
	}
	blockId := EventBlockId(blocks[:blockIdSize])
	blockHeader := EventBlock(blocks[blockIdSize : blockIdSize+blockHeaderSize])
	next := blockIdSize + blockHeaderSize + blockHeader.CompressedSize()
	block := EventBlock(blocks[blockIdSize:next])
	return blockId, block, blocks[next:]
}

type EventBlockId []byte // filename(12byte)|offset(8byte)

func (blockId EventBlockId) FileName() string {
	return string(blockId[:12])
}

func (blockId EventBlockId) Offset() uint64 {
	return binary.LittleEndian.Uint64(blockId[12:])
}

type EventBlock []byte // compressedSize(4byte)|uncompressedSize(4byte)|count(2byte)|minTimestamp(4byte)|maxTimestamp(4byte)|body

func (blk EventBlock) CompressedSize() uint32 {
	return binary.LittleEndian.Uint32(blk)
}
func (blk EventBlock) UncompressedSize() uint32 {
	return binary.LittleEndian.Uint32(blk[4:])
}
func (blk EventBlock) EntriesCount() uint16 {
	return binary.LittleEndian.Uint16(blk[8:])
}
func (blk EventBlock) MinCTS() uint32 {
	return binary.LittleEndian.Uint32(blk[10:])
}
func (blk EventBlock) MaxCTS() uint32 {
	return binary.LittleEndian.Uint32(blk[14:])
}
func (blk EventBlock) CompressedEventEntries() CompressedEventEntries {
	return CompressedEventEntries(blk[18:])
}
func (blk EventBlock) EventEntries() EventEntries {
	entries := make([]byte, blk.UncompressedSize())
	lz4.DecompressSafe(blk.CompressedEventEntries(), entries)
	return EventEntries(entries)
}

var fs vfs.Filesystem = vfs.OS()

type Store struct {
	Config         Config
	RootDir        string
	inputQueue     chan evtInput
	compressionBuf []byte
	currentFile    vfs.File
	currentTime    time.Time
	currentWindow  int64
	currentDiscr   discr.Discrminator
}

func NewStore(rootDir string) *Store {
	return &Store{
		Config:         defaultConfig,
		RootDir:        rootDir,
		inputQueue:     make(chan evtInput, 100),
		compressionBuf: make([]byte, 1024),
	}
}

func (store *Store) Start() error {
	err := os.MkdirAll(store.RootDir, 0777)
	if err != nil {
		countlog.Error("event!failed to create store dir", "rootDir", store.RootDir, "err", err)
		return err
	}
	go func() {
		for {
			store.flushInputQueue()
			store.clean()
			time.Sleep(store.Config.MaximumFlushInterval)
		}
	}()
	return nil
}

func (store *Store) clean() {
	defer func() {
		recovered := recover()
		if recovered != nil {
			countlog.Fatal("event!store.clean.panic", "err", recovered,
				"stacktrace", countlog.ProvideStacktrace)
		}
	}()
	files, err := fs.ReadDir(store.RootDir)
	if err != nil {
		countlog.Error("event!failed to read dir", "err", err, "rootDir", store.RootDir)
		return
	}
	if len(files) > store.Config.KeepFilesCount {
		for _, file := range files[:len(files)-store.Config.KeepFilesCount] {
			filePath := path.Join(store.RootDir, file.Name())
			err := fs.Remove(filePath)
			if err != nil {
				countlog.Error("event!failed to clean old file", "err", err, "filePath", filePath)
			} else {
				countlog.Info("event!cleaned_old_file", "filePath", filePath)
			}
		}
	}
}

func (store *Store) flushInputQueue() {
	startFlushTime := time.Now()
	totalEntriesCount := 0
	defer func() {
		if totalEntriesCount > 0 {
			countlog.Debug("event!store.flushed",
				"latency", time.Since(startFlushTime),
				"totalEntriesCount", totalEntriesCount)
		}
		recovered := recover()
		if recovered != nil {
			countlog.Fatal("event!store.flushInputQueue.panic", "err", recovered,
				"stacktrace", countlog.ProvideStacktrace)
		}
	}()
	tmpBuf := [4]byte{}
	blockBody := []byte{}
	for {
		shouldContinue, entriesCount := store.flushOnce(tmpBuf, blockBody)
		if !shouldContinue {
			break
		}
		totalEntriesCount += int(entriesCount)
	}
}

func (store *Store) flushOnce(tmpBuf [4]byte, blockBody []byte) (bool, uint16) {
	entriesCount := uint16(0)
	blockBody = blockBody[:0]
	minCTS := uint32(math.MaxUint32)
	maxCTS := uint32(0)
	for {
		select {
		case input := <-store.inputQueue:
			startProcessInputTime := time.Now()
			if input.eventTS.Sub(store.currentTime) > time.Hour && len(blockBody) > 0 {
				err := store.saveBlock(entriesCount, minCTS, maxCTS, blockBody)
				if err != nil {
					countlog.Error("event!failed to save block", "err", err)
					return false, entriesCount
				}
				entriesCount = uint16(0)
				blockBody = blockBody[:0]
				minCTS = uint32(math.MaxUint32)
				maxCTS = uint32(0)
			}
			if err := store.switchFile(input.eventTS); err != nil {
				countlog.Error("event!failed to switch file", "err", err)
				return false, entriesCount
			}
			scene := store.currentDiscr.SceneOf(input.eventBody)
			if scene == nil {
				continue
			}
			eventCTS := timeutil.Compress(store.currentTime, input.eventTS)
			if eventCTS > maxCTS {
				maxCTS = eventCTS
			}
			if eventCTS < minCTS {
				minCTS = eventCTS
			}
			entriesCount++
			binary.LittleEndian.PutUint32(tmpBuf[:], uint32(len(input.eventBody)))
			blockBody = append(blockBody, tmpBuf[:]...)
			binary.LittleEndian.PutUint32(tmpBuf[:], eventCTS)
			blockBody = append(blockBody, tmpBuf[:]...)
			blockBody = append(blockBody, input.eventBody...)
			countlog.Trace("event!store.added_event", "latency", time.Since(startProcessInputTime))
			if entriesCount > store.Config.BlockEntriesCountLimit {
				break
			}
			if len(blockBody) > store.Config.BlockSizeLimit {
				break
			}
			continue
		default:
			if len(blockBody) > 0 {
				break
			}
			return false, entriesCount
		}
		break
	}
	err := store.saveBlock(entriesCount, minCTS, maxCTS, blockBody)
	if err != nil {
		countlog.Error("event!failed to save block", "err", err)
		return false, entriesCount
	}
	return true, entriesCount
}

func (store *Store) saveBlock(entriesCount uint16, minCTS, maxCTS uint32, blockBody []byte) error {
	file := store.currentFile
	var blockHeader [blockHeaderSize]byte
	bound := lz4.CompressBound(len(blockBody))
	if len(store.compressionBuf) < bound {
		store.compressionBuf = make([]byte, bound)
	}
	compressedSize := lz4.CompressDefault(blockBody, store.compressionBuf)
	binary.LittleEndian.PutUint32(blockHeader[0:4], uint32(compressedSize))
	binary.LittleEndian.PutUint32(blockHeader[4:8], uint32(len(blockBody)))
	binary.LittleEndian.PutUint16(blockHeader[8:10], uint16(entriesCount))
	binary.LittleEndian.PutUint32(blockHeader[10:14], minCTS)
	binary.LittleEndian.PutUint32(blockHeader[14:blockHeaderSize], maxCTS)
	_, err := file.Write(blockHeader[:])
	if err != nil {
		return err
	}
	_, err = file.Write(store.compressionBuf[:compressedSize])
	if err != nil {
		return err
	}
	return nil
}

func (store *Store) switchFile(ts time.Time) error {
	window := ts.Unix() / 3600
	if window == store.currentWindow {
		return nil
	}
	store.currentDiscr = discr.NewDiscrminator()
	if store.currentFile != nil {
		if err := store.currentFile.Close(); err != nil {
			return err
		}
	}
	store.currentWindow = window
	store.currentTime = time.Unix(window*3600, 0)
	fileName := store.currentTime.Format(filenamePattern)
	file, err := fs.OpenFile(
		path.Join(store.RootDir, fileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		file, err = fs.OpenFile(
			path.Join(store.RootDir, fileName), os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return err
		}
	} else {
		header := [fileHeaderSize]byte{0xD1, 0xD1, 1, 0, 0, 0, 0}
		binary.LittleEndian.PutUint32(header[3:7], uint32(store.currentTime.Unix()))
		_, err = file.Write(header[:])
		if err != nil {
			return err
		}
	}
	file.Seek(0, io.SeekEnd)
	store.currentFile = file
	return nil
}

func (store *Store) Add(eventBody discr.EventBody) error {
	select {
	case store.inputQueue <- evtInput{
		eventBody: eventBody,
		eventTS:   timeutil.Now(),
	}:
		return nil
	default:
		return errors.New("input queue overflow")
	}
}

func (store *Store) List(startTime time.Time, endTime time.Time, skip int, limit int) (EventBlocks, error) {
	files, err := fs.ReadDir(store.RootDir)
	if err != nil {
		return nil, err
	}
	eventBlocks := bytes.NewBuffer(nil)
	var headerBuf = [blockHeaderSize]byte{}
	var header EventBlock = headerBuf[:]
	var fileHeader = [4]byte{}
	readEntriesCount := 0
	var copyBuf = [4096]byte{}
	for _, fileInfo := range files {
		filename := fileInfo.Name()
		fileTime, err := time.ParseInLocation(filenamePattern, filename, CST)
		if err != nil {
			continue
		}
		if fileTime.Add(time.Hour).Before(startTime) {
			countlog.Debug("event!skip_file_because_time_too_small",
				"fileTime", fileTime, "startTime", startTime)
			continue
		}
		if fileTime.After(endTime) {
			countlog.Debug("event!skip_file_because_time_too_large",
				"fileTime", fileTime, "endTime", endTime)
			continue
		}
		blockIdTmpl := []byte(filename)
		blockIdTmpl = append(blockIdTmpl, []byte{0, 0, 0, 0, 0, 0, 0, 0}...)
		file, err := fs.OpenFile(path.Join(store.RootDir, filename), os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		file.Seek(3, io.SeekStart)
		_, err = io.ReadFull(file, fileHeader[:])
		if err != nil {
			return nil, err
		}
		baseTime := time.Unix(int64(binary.LittleEndian.Uint32(fileHeader[:])), 0)
		for {
			_, err = io.ReadFull(file, header)
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			shouldSkip := readEntriesCount < skip
			if shouldSkip {
				readEntriesCount += int(header.EntriesCount())
			}
			minTime := timeutil.Decompress(baseTime, header.MinCTS())
			if minTime.After(endTime) {
				shouldSkip = true
			}
			maxTime := timeutil.Decompress(baseTime, header.MaxCTS())
			if maxTime.Before(startTime) {
				shouldSkip = true
			}
			if shouldSkip {
				file.Seek(int64(header.CompressedSize()), io.SeekCurrent)
				continue
			}
			offset, err := file.Seek(0, io.SeekCurrent)
			if err != nil {
				return nil, err
			}
			binary.LittleEndian.PutUint64(blockIdTmpl[12:], uint64(offset))
			_, err = eventBlocks.Write(blockIdTmpl)
			if err != nil {
				return nil, err
			}
			_, err = eventBlocks.Write(header)
			if err != nil {
				return nil, err
			}
			_, err = copyN(eventBlocks, file, int64(header.CompressedSize()), copyBuf[:])
			if err != nil {
				return nil, err
			}
			readEntriesCount += int(header.EntriesCount())
			if readEntriesCount > skip+limit {
				return EventBlocks(eventBlocks.Bytes()), nil
			}
		}
	}
	return EventBlocks(eventBlocks.Bytes()), nil
}

func copyN(dst io.Writer, src io.Reader, n int64, buf []byte) (written int64, err error) {
	written, err = io.CopyBuffer(dst, io.LimitReader(src, n), buf)
	if written == n {
		return n, nil
	}
	if written < n && err == nil {
		// src stopped early; must have been EOF.
		err = io.EOF
	}
	return
}
