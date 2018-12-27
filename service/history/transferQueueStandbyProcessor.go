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

package history

import (
	"github.com/uber-common/bark"
	workflow "github.com/uber/cadence/.gen/go/shared"
	"github.com/uber/cadence/client/matching"
	"github.com/uber/cadence/common"
	"github.com/uber/cadence/common/logging"
	"github.com/uber/cadence/common/messaging"
	"github.com/uber/cadence/common/metrics"
	"github.com/uber/cadence/common/persistence"
)

type (
	transferQueueStandbyProcessorImpl struct {
		clusterName        string
		shard              ShardContext
		historyService     *historyEngineImpl
		options            *QueueProcessorOptions
		executionManager   persistence.ExecutionManager
		cache              *historyCache
		transferTaskFilter transferTaskFilter
		logger             bark.Logger
		metricsClient      metrics.Client
		*transferQueueProcessorBase
		*queueProcessorBase
		queueAckMgr
	}
)

var (
	postActionNoOp = func() error { return nil }
)

func newTransferQueueStandbyProcessor(clusterName string, shard ShardContext, historyService *historyEngineImpl,
	visibilityMgr persistence.VisibilityManager, visibilityProducer messaging.Producer,
	matchingClient matching.Client, taskAllocator taskAllocator, logger bark.Logger) *transferQueueStandbyProcessorImpl {
	config := shard.GetConfig()
	options := &QueueProcessorOptions{
		StartDelay:                         config.TransferProcessorStartDelay,
		BatchSize:                          config.TransferTaskBatchSize,
		WorkerCount:                        config.TransferTaskWorkerCount,
		MaxPollRPS:                         config.TransferProcessorMaxPollRPS,
		MaxPollInterval:                    config.TransferProcessorMaxPollInterval,
		MaxPollIntervalJitterCoefficient:   config.TransferProcessorMaxPollIntervalJitterCoefficient,
		UpdateAckInterval:                  config.TransferProcessorUpdateAckInterval,
		UpdateAckIntervalJitterCoefficient: config.TransferProcessorUpdateAckIntervalJitterCoefficient,
		MaxRetryCount:                      config.TransferTaskMaxRetryCount,
		MetricScope:                        metrics.TransferStandbyQueueProcessorScope,
	}
	logger = logger.WithFields(bark.Fields{
		logging.TagWorkflowCluster: clusterName,
	})

	transferTaskFilter := func(task *persistence.TransferTaskInfo) (bool, error) {
		return taskAllocator.verifyStandbyTask(clusterName, task.DomainID, task)
	}
	maxReadAckLevel := func() int64 {
		return shard.GetTransferMaxReadLevel()
	}
	updateClusterAckLevel := func(ackLevel int64) error {
		return shard.UpdateTransferClusterAckLevel(clusterName, ackLevel)
	}
	transferQueueShutdown := func() error {
		return nil
	}

	processor := &transferQueueStandbyProcessorImpl{
		clusterName:        clusterName,
		shard:              shard,
		historyService:     historyService,
		options:            options,
		executionManager:   shard.GetExecutionManager(),
		cache:              historyService.historyCache,
		transferTaskFilter: transferTaskFilter,
		logger:             logger,
		metricsClient:      historyService.metricsClient,
		transferQueueProcessorBase: newTransferQueueProcessorBase(
			shard, options, visibilityMgr, visibilityProducer, matchingClient,
			maxReadAckLevel, updateClusterAckLevel, transferQueueShutdown, logger,
		),
	}

	queueAckMgr := newQueueAckMgr(shard, options, processor, shard.GetTransferClusterAckLevel(clusterName), logger)
	queueProcessorBase := newQueueProcessorBase(clusterName, shard, options, processor, queueAckMgr, logger)
	processor.queueAckMgr = queueAckMgr
	processor.queueProcessorBase = queueProcessorBase

	return processor
}

func (t *transferQueueStandbyProcessorImpl) notifyNewTask() {
	t.queueProcessorBase.notifyNewTask()
}

func (t *transferQueueStandbyProcessorImpl) process(qTask queueTaskInfo) (int, error) {
	task, ok := qTask.(*persistence.TransferTaskInfo)
	if !ok {
		return metrics.TransferStandbyQueueProcessorScope, errUnexpectedQueueTask
	}
	ok, err := t.transferTaskFilter(task)
	if err != nil {
		return metrics.TransferStandbyQueueProcessorScope, err
	} else if !ok {
		return metrics.TransferStandbyQueueProcessorScope, nil
	}

	switch task.TaskType {
	case persistence.TransferTaskTypeActivityTask:
		return metrics.TransferStandbyTaskActivityScope, t.processActivityTask(task)

	case persistence.TransferTaskTypeDecisionTask:
		return metrics.TransferStandbyTaskDecisionScope, t.processDecisionTask(task)

	case persistence.TransferTaskTypeCloseExecution:
		return metrics.TransferStandbyTaskCloseExecutionScope, t.processCloseExecution(task)

	case persistence.TransferTaskTypeCancelExecution:
		return metrics.TransferStandbyTaskCancelExecutionScope, t.processCancelExecution(task)

	case persistence.TransferTaskTypeSignalExecution:
		return metrics.TransferStandbyTaskSignalExecutionScope, t.processSignalExecution(task)

	case persistence.TransferTaskTypeStartChildExecution:
		return metrics.TransferStandbyTaskStartChildExecutionScope, t.processStartChildExecution(task)

	default:
		return metrics.TransferStandbyQueueProcessorScope, errUnknownTransferTask
	}
}

func (t *transferQueueStandbyProcessorImpl) processActivityTask(transferTask *persistence.TransferTaskInfo) error {

	var activityScheduleToStartTimeout *int32
	processTaskIfClosed := false
	return t.processTransfer(processTaskIfClosed, transferTask, func(msBuilder mutableState) error {
		activityInfo, isPending := msBuilder.GetActivityInfo(transferTask.ScheduleID)

		if !isPending {
			return nil
		}
		ok, err := verifyTaskVersion(t.shard, t.logger, transferTask.DomainID, activityInfo.Version, transferTask.Version, transferTask)
		if err != nil {
			return err
		} else if !ok {
			return nil
		}

		now := t.shard.GetCurrentTime(t.clusterName)
		pushToMatching := now.Sub(transferTask.GetVisibilityTimestamp()) > t.shard.GetConfig().StandbyClusterDelay()
		if activityInfo.StartedID == common.EmptyEventID {
			if !pushToMatching {
				return ErrTaskRetry
			}

			activityScheduleToStartTimeout = common.Int32Ptr(common.MinInt32(activityInfo.ScheduleToStartTimeout, common.MaxTaskTimeout))
			return nil
		}

		return nil
	}, func() error {
		if activityScheduleToStartTimeout == nil {
			return nil
		}

		timeout := common.MinInt32(*activityScheduleToStartTimeout, common.MaxTaskTimeout)
		err := t.pushActivity(transferTask, timeout)
		return err
	})
}

func (t *transferQueueStandbyProcessorImpl) processDecisionTask(transferTask *persistence.TransferTaskInfo) error {

	var decisionScheduleToStartTimeout *int32
	var tasklist *workflow.TaskList
	processTaskIfClosed := false

	execution := workflow.WorkflowExecution{
		WorkflowId: common.StringPtr(transferTask.WorkflowID),
		RunId:      common.StringPtr(transferTask.RunID),
	}

	return t.processTransfer(processTaskIfClosed, transferTask, func(msBuilder mutableState) error {
		decisionInfo, isPending := msBuilder.GetPendingDecision(transferTask.ScheduleID)

		executionInfo := msBuilder.GetExecutionInfo()
		workflowTimeout := executionInfo.WorkflowTimeout
		decisionTimeout := common.MinInt32(workflowTimeout, common.MaxTaskTimeout)
		wfTypeName := executionInfo.WorkflowTypeName
		startTimestamp := executionInfo.StartTimestamp

		markWorkflowAsOpen := transferTask.ScheduleID <= common.FirstEventID+2

		if !isPending {
			if markWorkflowAsOpen {
				err := t.recordWorkflowStarted(transferTask.DomainID, execution, wfTypeName, startTimestamp.UnixNano(), workflowTimeout, executionInfo.NextEventID)
				if err != nil {
					return err
				}
			}
			return nil
		}

		ok, err := verifyTaskVersion(t.shard, t.logger, transferTask.DomainID, decisionInfo.Version, transferTask.Version, transferTask)
		if err != nil {
			return err
		} else if !ok {
			return nil
		}

		if markWorkflowAsOpen {
			err = t.recordWorkflowStarted(transferTask.DomainID, execution, wfTypeName, startTimestamp.UnixNano(), workflowTimeout, executionInfo.NextEventID)
		}

		now := t.shard.GetCurrentTime(t.clusterName)
		pushToMatching := now.Sub(transferTask.GetVisibilityTimestamp()) > t.shard.GetConfig().StandbyClusterDelay()
		if decisionInfo.StartedID == common.EmptyEventID {
			if !pushToMatching {
				return ErrTaskRetry
			}

			decisionScheduleToStartTimeout = common.Int32Ptr(decisionTimeout)
			tasklist = &workflow.TaskList{Name: &transferTask.TaskList}
			return nil
		}

		return nil
	}, func() error {
		if decisionScheduleToStartTimeout == nil {
			return nil
		}

		timeout := common.MinInt32(*decisionScheduleToStartTimeout, common.MaxTaskTimeout)
		err := t.pushDecision(transferTask, tasklist, timeout)
		return err
	})
}

func (t *transferQueueStandbyProcessorImpl) processCloseExecution(transferTask *persistence.TransferTaskInfo) error {

	processTaskIfClosed := true

	execution := workflow.WorkflowExecution{
		WorkflowId: common.StringPtr(transferTask.WorkflowID),
		RunId:      common.StringPtr(transferTask.RunID),
	}

	return t.processTransfer(processTaskIfClosed, transferTask, func(msBuilder mutableState) error {

		if msBuilder.IsWorkflowExecutionRunning() {
			// this can happen if workflow is reset.
			return nil
		}

		executionInfo := msBuilder.GetExecutionInfo()
		workflowTypeName := executionInfo.WorkflowTypeName
		workflowStartTimestamp := executionInfo.StartTimestamp.UnixNano()
		workflowCloseTimestamp := msBuilder.GetLastUpdatedTimestamp()
		workflowCloseStatus := getWorkflowExecutionCloseStatus(executionInfo.CloseStatus)
		workflowHistoryLength := msBuilder.GetNextEventID()

		ok, err := verifyTaskVersion(t.shard, t.logger, transferTask.DomainID, msBuilder.GetLastWriteVersion(), transferTask.Version, transferTask)
		if err != nil {
			return err
		} else if !ok {
			return nil
		}

		// DO NOT REPLY TO PARENT
		// since event replication should be done by active cluster

		return t.recordWorkflowClosed(
			transferTask.DomainID, execution, workflowTypeName, workflowStartTimestamp, workflowCloseTimestamp, workflowCloseStatus, workflowHistoryLength, executionInfo.NextEventID,
		)
	}, postActionNoOp)
}

func (t *transferQueueStandbyProcessorImpl) processCancelExecution(transferTask *persistence.TransferTaskInfo) error {

	processTaskIfClosed := false
	return t.processTransfer(processTaskIfClosed, transferTask, func(msBuilder mutableState) error {
		requestCancelInfo, isPending := msBuilder.GetRequestCancelInfo(transferTask.ScheduleID)

		if !isPending {
			return nil
		}
		ok, err := verifyTaskVersion(t.shard, t.logger, transferTask.DomainID, requestCancelInfo.Version, transferTask.Version, transferTask)
		if err != nil {
			return err
		} else if !ok {
			return nil
		}

		if t.discardTask(transferTask) {
			return ErrTaskDiscarded
		}

		return ErrTaskRetry
	}, postActionNoOp)
}

func (t *transferQueueStandbyProcessorImpl) processSignalExecution(transferTask *persistence.TransferTaskInfo) error {

	processTaskIfClosed := false
	return t.processTransfer(processTaskIfClosed, transferTask, func(msBuilder mutableState) error {
		signalInfo, isPending := msBuilder.GetSignalInfo(transferTask.ScheduleID)

		if !isPending {
			return nil
		}
		ok, err := verifyTaskVersion(t.shard, t.logger, transferTask.DomainID, signalInfo.Version, transferTask.Version, transferTask)
		if err != nil {
			return err
		} else if !ok {
			return nil
		}

		if t.discardTask(transferTask) {
			return ErrTaskDiscarded
		}

		return ErrTaskRetry
	}, postActionNoOp)
}

func (t *transferQueueStandbyProcessorImpl) processStartChildExecution(transferTask *persistence.TransferTaskInfo) error {

	processTaskIfClosed := false
	return t.processTransfer(processTaskIfClosed, transferTask, func(msBuilder mutableState) error {
		childWorkflowInfo, isPending := msBuilder.GetChildExecutionInfo(transferTask.ScheduleID)

		if !isPending {
			return nil
		}
		ok, err := verifyTaskVersion(t.shard, t.logger, transferTask.DomainID, childWorkflowInfo.Version, transferTask.Version, transferTask)
		if err != nil {
			return err
		} else if !ok {
			return nil
		}

		if childWorkflowInfo.StartedID == common.EmptyEventID {

			if t.discardTask(transferTask) {
				return ErrTaskDiscarded
			}

			return ErrTaskRetry
		}
		return nil
	}, postActionNoOp)
}

func (t *transferQueueStandbyProcessorImpl) processTransfer(processTaskIfClosed bool, transferTask *persistence.TransferTaskInfo,
	action func(mutableState) error, postAction func() error) (retError error) {
	context, release, err := t.cache.getOrCreateWorkflowExecution(t.getDomainIDAndWorkflowExecution(transferTask))
	if err != nil {
		return err
	}
	defer func() {
		if retError == ErrTaskRetry {
			release(nil)
		} else {
			release(retError)
		}
	}()

	msBuilder, err := loadMutableStateForTransferTask(context, transferTask, t.metricsClient, t.logger)
	if err != nil {
		return err
	} else if msBuilder == nil {
		return nil
	}

	if !processTaskIfClosed && !msBuilder.IsWorkflowExecutionRunning() {
		// workflow already finished, no need to process the timer
		return nil
	}

	err = action(msBuilder)
	if err != nil {
		return err
	}

	release(nil)
	err = postAction()
	return err
}

func (t *transferQueueStandbyProcessorImpl) getDomainIDAndWorkflowExecution(transferTask *persistence.TransferTaskInfo) (string, workflow.WorkflowExecution) {
	return transferTask.DomainID, workflow.WorkflowExecution{
		WorkflowId: common.StringPtr(transferTask.WorkflowID),
		RunId:      common.StringPtr(transferTask.RunID),
	}
}

func (t *transferQueueStandbyProcessorImpl) discardTask(transferTask *persistence.TransferTaskInfo) bool {
	// the current time got from shard is already delayed by t.shard.GetConfig().StandbyClusterDelay()
	// so discard will be true if task is delayed by 2*t.shard.GetConfig().StandbyClusterDelay()
	now := t.shard.GetCurrentTime(t.clusterName)
	discard := now.Sub(transferTask.GetVisibilityTimestamp()) > t.shard.GetConfig().StandbyClusterDelay()
	if discard {
		t.logger.WithFields(bark.Fields{
			logging.TagDomainID:            transferTask.DomainID,
			logging.TagWorkflowExecutionID: transferTask.WorkflowID,
			logging.TagWorkflowRunID:       transferTask.RunID,
			logging.TagTaskID:              transferTask.GetTaskID(),
			logging.TagTaskType:            transferTask.GetTaskType(),
			logging.TagVersion:             transferTask.GetVersion(),
			logging.TagTimestamp:           transferTask.VisibilityTimestamp,
			logging.TagEventID:             transferTask.ScheduleID,
		}).Error("Discarding standby transfer task due to task being pending for too long.")
	}
	return discard
}
