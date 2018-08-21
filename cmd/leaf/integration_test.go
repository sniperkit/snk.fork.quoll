/*
Sniperkit-Bot
- Status: analyzed
*/

package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sniperkit/snk.fork.quoll/evtstore"
)

func Test_list(t *testing.T) {
	should := require.New(t)
	resp, err := http.Get("http://127.0.0.1:8005/list-events")
	should.Nil(err)
	body, err := ioutil.ReadAll(resp.Body)
	should.Nil(err)
	blocks := evtstore.EventBlocks(body)
	blockId, _, _ := blocks.Next()
	should.Len(blockId.FileName(), 12)
}

func Test_add(t *testing.T) {
	should := require.New(t)
	resp, err := http.Post("http://127.0.0.1:8005/update-session-matcher",
		"application/json", bytes.NewBufferString(`
		{
			"SessionType": "/gulfstream/passenger/v2/core/pNewOrder",
			"KeepNSessionsPerScene": 10240,
			"CallOutbounds": [
				{
					"ServiceName": "Carrera",
					"RequestPatterns": {"product_id": "\"product_id\":(\\d+)"}
				}
			]
		}
	`))
	should.Nil(err)
	respBody, err := ioutil.ReadAll(resp.Body)
	should.Nil(err)
	should.Equal(`{"errno":0}`, string(respBody))
	files, err := ioutil.ReadDir("/home/xiaoju/testdata2")
	should.Nil(err)
	contents := [][]byte{}
	totalSize := 0
	for _, file := range files[:1024] {
		content, err := ioutil.ReadFile(path.Join("/home/xiaoju/testdata2", file.Name()))
		should.Nil(err)
		contents = append(contents, content)
		totalSize += len(content)
	}
	before := time.Now()
	for _, content := range contents {
		resp, err := http.Post("http://127.0.0.1:8005/add-event", "application/json", bytes.NewBuffer(content))
		should.Nil(err)
		respBody, err := ioutil.ReadAll(resp.Body)
		should.Nil(err)
		if string(respBody) != `{"errno":0}` {
			time.Sleep(time.Second)
			resp, err := http.Post("http://127.0.0.1:8005/add-event", "application/json", bytes.NewBuffer(content))
			should.Nil(err)
			respBody, err := ioutil.ReadAll(resp.Body)
			should.Nil(err)
			should.Equal(`{"errno":0}`, string(respBody))
		}
	}
	after := time.Now()
	fmt.Println(totalSize)
	fmt.Println(len(contents))
	fmt.Println(after.Sub(before))
}
