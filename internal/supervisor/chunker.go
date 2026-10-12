package supervisor

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rachmanzz/subzero/pkg/protocol"
)

// TaskRunner defines an abstraction for executing a single TaskMessage (e.g., worker RPC client or local handler).
type TaskRunner interface {
	RunTask(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error)
}

// TaskRunnerFunc adapts a function into a TaskRunner.
type TaskRunnerFunc func(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error)

func (f TaskRunnerFunc) RunTask(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error) {
	return f(ctx, task)
}

// ChunkSpec represents a sliced partition of a massive document.
type ChunkSpec struct {
	Index        int
	SheetName    string // Target sheet (if multi-sheet)
	RowStart     int64  // 1-based start row index
	RowCount     int64  // Number of rows in chunk
	CustomConfig map[string]string
}

// ChunkFanOutPlan specifies how a massive document TaskMessage is split and parallelized.
type ChunkFanOutPlan struct {
	BaseTask protocol.TaskMessage
	Chunks   []ChunkSpec
}

// BuildChunkFanOutPlan inspects a TaskMessage's CustomConfig to determine if chunking is requested.
// Supported CustomConfig keys:
//   - "sheets": comma-separated list of sheet names (e.g. "Sheet1,Sheet2,Summary")
//   - "chunk_rows": chunk batch size (e.g. "1000")
//   - "total_rows": total row count for range chunking (e.g. "5000")
func BuildChunkFanOutPlan(task *protocol.TaskMessage) (*ChunkFanOutPlan, bool) {
	if task == nil || task.CustomConfig == nil {
		return nil, false
	}

	plan := &ChunkFanOutPlan{
		BaseTask: *task,
	}

	// 1. Check multi-sheet chunking
	if sheetsVal, ok := task.CustomConfig["sheets"]; ok && sheetsVal != "" {
		sheets := strings.Split(sheetsVal, ",")
		var validSheets []string
		for _, s := range sheets {
			trimmed := strings.TrimSpace(s)
			if trimmed != "" {
				validSheets = append(validSheets, trimmed)
			}
		}

		if len(validSheets) > 1 {
			for idx, sheet := range validSheets {
				cfg := make(map[string]string)
				for k, v := range task.CustomConfig {
					cfg[k] = v
				}
				cfg["sheet"] = sheet
				delete(cfg, "sheets")

				plan.Chunks = append(plan.Chunks, ChunkSpec{
					Index:        idx,
					SheetName:    sheet,
					CustomConfig: cfg,
				})
			}
			return plan, true
		}
	}

	// 2. Check row range chunking
	chunkRowsStr, hasChunkRows := task.CustomConfig["chunk_rows"]
	totalRowsStr, hasTotalRows := task.CustomConfig["total_rows"]
	if hasChunkRows && hasTotalRows {
		chunkRows, err1 := strconv.ParseInt(chunkRowsStr, 10, 64)
		totalRows, err2 := strconv.ParseInt(totalRowsStr, 10, 64)
		if err1 == nil && err2 == nil && chunkRows > 0 && totalRows > chunkRows {
			var chunkIdx int
			for start := int64(1); start <= totalRows; start += chunkRows {
				count := chunkRows
				if start+count-1 > totalRows {
					count = totalRows - start + 1
				}

				cfg := make(map[string]string)
				for k, v := range task.CustomConfig {
					cfg[k] = v
				}
				cfg["row_start"] = strconv.FormatInt(start, 10)
				cfg["row_count"] = strconv.FormatInt(count, 10)

				plan.Chunks = append(plan.Chunks, ChunkSpec{
					Index:        chunkIdx,
					RowStart:     start,
					RowCount:     count,
					CustomConfig: cfg,
				})
				chunkIdx++
			}
			return plan, true
		}
	}

	return nil, false
}

// ChunkedTask wraps a massive document task and coordinates its fan-out execution via SubWorkerPool.
type ChunkedTask struct {
	plan   *ChunkFanOutPlan
	runner TaskRunner
	result *protocol.ResultMessage
	mu     sync.Mutex
}

// NewChunkedTask creates an executable Task with chunk fan-out support.
func NewChunkedTask(plan *ChunkFanOutPlan, runner TaskRunner) *ChunkedTask {
	return &ChunkedTask{
		plan:   plan,
		runner: runner,
	}
}

func (c *ChunkedTask) Name() string {
	return fmt.Sprintf("chunk-fanout-%s", c.plan.BaseTask.TaskID)
}

// Execute fans out the chunks across the SubWorkerPool and aggregates results (fan-in).
func (c *ChunkedTask) Execute(ctx context.Context, pool SubWorkerPool) error {
	start := time.Now()
	var totalProcessed atomic.Int64
	var firstErr error
	var errOnce sync.Once

	for _, chunk := range c.plan.Chunks {
		chunk := chunk
		pool.Go(func(subCtx context.Context) error {
			subTask := c.plan.BaseTask
			subTask.TaskID = fmt.Sprintf("%s-chunk-%d", c.plan.BaseTask.TaskID, chunk.Index)
			subTask.CustomConfig = chunk.CustomConfig

			res, err := c.runner.RunTask(subCtx, &subTask)
			if err != nil {
				errOnce.Do(func() { firstErr = err })
				return err
			}
			if res != nil && !res.IsSuccess {
				err = fmt.Errorf("chunk %d failed: %s", chunk.Index, res.ErrorMessage)
				errOnce.Do(func() { firstErr = err })
				return err
			}

			if res != nil {
				totalProcessed.Add(res.ProcessedRows)
			}
			return nil
		})
	}

	// Wait for pool completion
	waitErr := pool.Wait()
	elapsed := time.Since(start)

	c.mu.Lock()
	defer c.mu.Unlock()

	if waitErr != nil || firstErr != nil {
		errToReport := waitErr
		if errToReport == nil {
			errToReport = firstErr
		}
		c.result = &protocol.ResultMessage{
			TaskID:        c.plan.BaseTask.TaskID,
			IsSuccess:     false,
			ProcessedRows: totalProcessed.Load(),
			ErrorMessage:  errToReport.Error(),
			ExecutionTime: elapsed.String(),
		}
		return errToReport
	}

	c.result = &protocol.ResultMessage{
		TaskID:        c.plan.BaseTask.TaskID,
		IsSuccess:     true,
		ProcessedRows: totalProcessed.Load(),
		ExecutionTime: elapsed.String(),
	}
	return nil
}

// Result returns the aggregated ResultMessage after execution.
func (c *ChunkedTask) Result() *protocol.ResultMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}
