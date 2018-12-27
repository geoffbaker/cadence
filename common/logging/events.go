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

package logging

// Events
const (
	// Global Events

	// HistoryBuilder events
	InvalidHistoryActionEventID = 1000

	// History Engine events
	HistoryEngineStarting              = 2000
	HistoryEngineStarted               = 2001
	HistoryEngineShuttingDown          = 2002
	HistoryEngineShutdown              = 2003
	PersistentStoreErrorEventID        = 2010
	HistorySerializationErrorEventID   = 2020
	HistoryDeserializationErrorEventID = 2021
	DuplicateTaskEventID               = 2030
	MultipleCompletionDecisionsEventID = 2040
	DuplicateTransferTaskEventID       = 2050
	DecisionFailedEventID              = 2060

	// Transfer Queue Processor events
	TransferQueueProcessorStarting         = 2100
	TransferQueueProcessorStarted          = 2101
	TransferQueueProcessorShuttingDown     = 2102
	TransferQueueProcessorShutdown         = 2103
	TransferQueueProcessorShutdownTimedout = 2104
	TransferTaskProcessingFailed           = 2105

	// Shard context events
	ShardRangeUpdatedEventID     = 3000
	ShardAllocateTimerBeforeRead = 3001

	// ShardController events
	ShardControllerStarted          = 4000
	ShardControllerShutdown         = 4001
	ShardControllerShuttingDown     = 4002
	ShardControllerShutdownTimedout = 4003
	RingMembershipChangedEvent      = 4004
	ShardClosedEvent                = 4005
	ShardItemCreated                = 4010
	ShardItemRemoved                = 4011
	ShardEngineCreating             = 4020
	ShardEngineCreated              = 4021
	ShardEngineStopping             = 4022
	ShardEngineStopped              = 4023

	// MutableSateBuilder events
	InvalidMutableStateActionEventID = 4100

	// Matching Engine events
	TaskListLoading       = 5000
	TaskListLoaded        = 5001
	TaskListUnloading     = 5002
	TaskListUnloaded      = 5003
	TaskListLoadingFailed = 5004

	// Query task events
	InvalidQueryTaskEventID = 6000
	QueryTaskFailedEventID  = 6001

	// Worker Service Events
	ReplicationTaskProcessorStarting         = 7100
	ReplicationTaskProcessorStarted          = 7101
	ReplicationTaskProcessorStartFailed      = 7102
	ReplicationTaskProcessorShuttingDown     = 7103
	ReplicationTaskProcessorShutdown         = 7104
	ReplicationTaskProcessorShutdownTimedout = 7105
	ReplicationTaskProcessingFailed          = 7106
	IndexProcessorStarting                   = 7200
	IndexProcessorStarted                    = 7201
	IndexProcessorStartFailed                = 7202
	IndexProcessorShuttingDown               = 7203
	IndexProcessorShutDown                   = 7204
	IndexProcessorShuttingDownTimedout       = 7205

	// General purpose events
	OperationFailed   = 9000
	OperationStuck    = 9001
	OperationCritical = 9002
)

// Tag values
const (
	// ListWorkflow FilterType values
	ListWorkflowFilterByID     = "WID"
	ListWorkflowFilterByType   = "WType"
	ListWorkflowFilterByStatus = "status"
)
