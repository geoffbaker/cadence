// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package indexer

import (
	"context"
	"encoding/json"
	"github.com/olivere/elastic"
	"github.com/uber-common/bark"
	"github.com/uber/cadence/.gen/go/indexer"
	"github.com/uber/cadence/common"
	"github.com/uber/cadence/common/collection"
	"github.com/uber/cadence/common/logging"
	"github.com/uber/cadence/common/messaging"
	"github.com/uber/cadence/common/metrics"
	"time"
)

// ESProcessor is interface for elastic search bulk processor
type ESProcessor interface {
	// Stop processor and clean up
	Stop()
	// Add request to bulk, and record kafka message in map with provided key
	Add(request elastic.BulkableRequest, key string, kafkaMsg messaging.Message)
}

// esProcessorImpl is agent of elastic.BulkProcessor
type esProcessorImpl struct {
	processor     *elastic.BulkProcessor
	mapToKafkaMsg collection.ConcurrentTxMap // key: esDocID+visType
	config        *Config
	logger        bark.Logger
	metricsClient metrics.Client
}

const (
	esRequestTypeIndex  = "index"
	esRequestTypeUpdate = "update"
	esRequestTypeDelete = "delete"

	// retry configs for es bulk processor
	esProcessorInitialRetryInterval = 200 * time.Millisecond
	esProcessorMaxRetryInterval     = 20 * time.Second
)

// NewESProcessorAndStart create new ESProcessor and start
func NewESProcessorAndStart(config *Config, client *elastic.Client, processorName string,
	logger bark.Logger, metricsClient metrics.Client) (ESProcessor, error) {
	p := &esProcessorImpl{
		config: config,
		logger: logger.WithFields(bark.Fields{
			logging.TagWorkflowComponent: logging.TagValueIndexerESProcessorComponent,
		}),
		metricsClient: metricsClient,
	}

	processor, err := client.BulkProcessor().
		Name(processorName).
		Workers(config.ESProcessorNumOfWorkers()).
		BulkActions(config.ESProcessorBulkActions()).
		BulkSize(config.ESProcessorBulkSize()).
		FlushInterval(config.ESProcessorFlushInterval()).
		Backoff(elastic.NewExponentialBackoff(esProcessorInitialRetryInterval, esProcessorMaxRetryInterval)).
		After(p.bulkAfterAction).
		Do(context.Background())
	if err != nil {
		return nil, err
	}

	p.processor = processor
	p.mapToKafkaMsg = collection.NewShardedConcurrentTxMap(1024, p.hashFn)
	return p, nil
}

func (p *esProcessorImpl) Stop() {
	p.processor.Stop()
	p.mapToKafkaMsg = nil
}

// Add an ES request, and an map item for kafka message
func (p *esProcessorImpl) Add(request elastic.BulkableRequest, key string, kafkaMsg messaging.Message) {
	if p.mapToKafkaMsg.Contains(key) {
		kafkaMsg.Ack() // duplicate message
		return
	}
	p.mapToKafkaMsg.Put(key, kafkaMsg)
	p.processor.Add(request)
}

// bulkAfterAction is triggered after bulk processor commit
func (p *esProcessorImpl) bulkAfterAction(id int64, requests []elastic.BulkableRequest, response *elastic.BulkResponse, err error) {
	if err != nil {
		// This happens after configured retry, which means something bad happens on cluster or index
		// We sleep then check until things go right or we die
		p.logger.WithFields(bark.Fields{
			logging.TagErr: err,
		}).Error("Error commit bulk request.")
		p.metricsClient.IncCounter(metrics.ESProcessorScope, metrics.ESProcessorFailures)

		time.Sleep(p.config.ESProcessorRetryInterval())
		for _, request := range requests {
			req, err := request.Source()
			if err != nil {
				p.logger.WithFields(bark.Fields{
					logging.TagErr:       err,
					logging.TagESRequest: request.String(),
				}).Error("Get request source err.")
				p.metricsClient.IncCounter(metrics.ESProcessorScope, metrics.ESProcessorCorruptedData)
				continue
			}
			if r := p.getESRequest(req); r != nil {
				p.processor.Add(r)
			}
		}
		return
	}

	responseItems := response.Items
	for i := 0; i < len(requests); i++ {
		req, err := requests[i].Source()
		if err != nil {
			p.logger.WithFields(bark.Fields{
				logging.TagErr:       err,
				logging.TagESRequest: requests[i].String(),
			}).Error("Get request source err.")
			p.metricsClient.IncCounter(metrics.ESProcessorScope, metrics.ESProcessorCorruptedData)
			continue
		}

		var reqHead map[string]map[string]interface{}
		if err := json.Unmarshal([]byte(req[0]), &reqHead); err != nil {
			p.logger.WithFields(bark.Fields{
				logging.TagErr: err,
			}).Error("Unmarshal request err.")
			p.metricsClient.IncCounter(metrics.ESProcessorScope, metrics.ESProcessorCorruptedData)
			continue
		}
		var head string
		for _, header := range reqHead {
			head = convertESVersionToVisibilityMsgType(header["version"].(float64))
		}

		responseItem := responseItems[i]
		for _, res := range responseItem {
			key := res.Id + head
			if res.Status >= 200 && res.Status < 300 {
				// success, ack kafka msg
				p.ackKafkaMsg(key)
			} else if res.Status == 409 {
				// version conflict, discard
				p.ackKafkaMsg(key)
				//p.logger.Info("version conflict")
			} else {
				// re-enqueue error request to retry until success
				if r := p.getESRequest(req); r != nil {
					p.processor.Add(r)
				}
			}
		}
	}
}

func (p *esProcessorImpl) ackKafkaMsg(key string) {
	msg, ok := p.mapToKafkaMsg.Get(key)
	if !ok {
		return // duplicate kafka message
	}
	kafkaMsg, ok := msg.(messaging.Message)
	if !ok { // must be bug in code and bad deployment
		p.logger.WithFields(bark.Fields{
			logging.TagESKey: key,
		}).Fatal("Message is not kafka message.")
	}

	kafkaMsg.Ack()
	//p.logger.Info("ack kafka message with key:", key)
	p.mapToKafkaMsg.Remove(key)
}

func (p *esProcessorImpl) hashFn(key interface{}) uint32 {
	id, ok := key.(string)
	if !ok {
		return 0
	}
	numOfShards := p.config.IndexerConcurrency()
	return uint32(common.WorkflowIDToHistoryShard(id, numOfShards))
}

// getESRequest from elastic.BulkableRequest.Source()
func (p *esProcessorImpl) getESRequest(input []string) elastic.BulkableRequest {
	var reqHead map[string]map[string]interface{}
	if err := json.Unmarshal([]byte(input[0]), &reqHead); err != nil {
		p.logger.WithFields(bark.Fields{
			logging.TagErr: err,
		}).Error("Unmarshal request err.")
		p.metricsClient.IncCounter(metrics.ESProcessorScope, metrics.ESProcessorCorruptedData)
		return nil
	}

	var index string
	var id string
	var typ string
	var version int64
	var versionType string
	for _, m := range reqHead {
		index = m["_index"].(string)
		id = m["_id"].(string)
		typ = m["_type"].(string)
		version = int64(m["version"].(float64))
		versionType = m["version_type"].(string)
	}

	var doc indexer.VisibilityMsg
	if len(input) == 2 {
		if err := json.Unmarshal([]byte(input[1]), &doc); err != nil {
			p.logger.WithFields(bark.Fields{
				logging.TagErr: err,
			}).Error("Unmarshal request body err.")
			return nil
		}
	}

	switch getReqType(reqHead) {
	case esRequestTypeIndex:
		return elastic.NewBulkIndexRequest().
			Index(index).
			Type(typ).
			Id(id).
			VersionType(versionType).
			Version(version).
			Doc(doc)
	case esRequestTypeDelete:
	}

	return nil
}

func getReqType(input map[string]map[string]interface{}) string {
	for k := range input {
		return k
	}
	return ""
}

func convertESVersionToVisibilityMsgType(version float64) string {
	switch version {
	case float64(versionForOpen):
		return indexer.VisibilityMsgTypeOpen.String()
	case float64(versionForClose):
		return indexer.VisibilityMsgTypeClosed.String()
	case float64(versionForDelete):
		return indexer.VisibilityMsgTypeDelete.String()
	}
	return ""
}
