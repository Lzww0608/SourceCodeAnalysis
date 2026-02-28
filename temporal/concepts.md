# 1. 核心概念 (Core Concepts)

Temporal 是一个持久化执行（Durable Execution）平台，Go SDK 提供了开发分布式应用的四个核心组件：

| 组件 | 说明 | 特性 |
|------|------|------|
| **Workflow** | 编排业务逻辑的协调者。 | • 确定性 (Deterministic)：代码必须在重放（Replay）时产生完全相同的执行路径。<br>• 持久化：状态由 Temporal Server 维护，进程崩溃后可从断点恢复。 |
| **Activity** | 执行具体业务逻辑（如数据库操作、API调用）。 | • 非确定性：可以包含任意代码（网络请求、I/O）。<br>• 幂等性：建议设计为幂等，因为可能会因超时或失败而重试。 |
| **Worker** | 托管 Workflow 和 Activity 代码的进程。 | 监听 Task Queue，从 Server 拉取任务并执行。 |
| **Client** | 用于启动、查询、向 Workflow 发送信号的入口。 | 通常在 API Handler 或其他服务中调用。 |

---

# 2. 快速开始 (Quick Start)

## 2.1 定义 Activity

Activity 是普通的 Go 函数或结构体方法。它接受标准的 `context.Context`。

```go
package myapp

import (
    "context"
    "fmt"
)

type MyActivities struct{}

func (a *MyActivities) SendEmail(ctx context.Context, userID string, subject string) (string, error) {
    fmt.Printf("Sending email to %s with subject %s\n", userID, subject)
    return "sent", nil
}
```

## 2.2 定义 Workflow

Workflow 是特殊的 Go 函数。必须使用 `workflow.Context`，且严禁使用 Go 原生的非确定性功能（如 `time.Now()`, `go routine`, `select`, `map` 迭代顺序等）。

```go
package myapp

import (
    "time"
    "go.temporal.io/sdk/temporal"
    "go.temporal.io/sdk/workflow"
)

func OrderWorkflow(ctx workflow.Context, userID string) (string, error) {
    // 1. 配置 Activity 选项（超时、重试策略）
    ao := workflow.ActivityOptions{
        StartToCloseTimeout: 10 * time.Second,
        RetryPolicy: &temporal.RetryPolicy{
            MaximumAttempts: 3, // 最多重试3次
        },
    }
    ctx = workflow.WithActivityOptions(ctx, ao)

    // 2. 执行 Activity
    var result string
    var a *MyActivities
    err := workflow.ExecuteActivity(ctx, a.SendEmail, userID, "Order Created").Get(ctx, &result)
    if err != nil {
        return "", err
    }

    // 3. 使用 Temporal 内置的 Sleep (不要用 time.Sleep)
    workflow.Sleep(ctx, 5*time.Second)

    return "workflow_completed_" + result, nil
}
```

## 2.3 启动 Worker

Worker 需要连接到 Temporal Server，并注册它支持的 Workflow 和 Activity。

```go
package main

import (
    "log"
    "go.temporal.io/sdk/client"
    "go.temporal.io/sdk/worker"
    "myapp"
)

func main() {
    // 1. 创建 Client
    c, err := client.Dial(client.Options{})
    if err != nil {
        log.Fatalln("Unable to create client", err)
    }
    defer c.Close()

    // 2. 创建 Worker，监听特定任务队列
    w := worker.New(c, "my-task-queue", worker.Options{})

    // 3. 注册 Workflow 和 Activity
    w.RegisterWorkflow(myapp.OrderWorkflow)
    w.RegisterActivity(&myapp.MyActivities{})

    // 4. 启动 Worker (阻塞运行)，非阻塞运行为 Start()
    err = w.Run(worker.InterruptCh())
    if err != nil {
        log.Fatalln("Unable to start worker", err)
    }
}
```

## 2.4 启动 Workflow

通常在 HTTP Handler 或其他触发器中执行。

```go
package main

import (
    "context"
    "log"
    "go.temporal.io/sdk/client"
    "myapp"
)

func StartOrder(orderID string) {
    c, _ := client.Dial(client.Options{})
    defer c.Close()

    options := client.StartWorkflowOptions{
        ID:        "order_" + orderID, // 业务ID，防止重复执行
        TaskQueue: "my-task-queue",    // 必须与 Worker 监听的队列一致
    }

    // 异步启动 Workflow
    we, err := c.ExecuteWorkflow(context.Background(), options, myapp.OrderWorkflow, "user_123")
    if err != nil {
        log.Fatalln("Unable to execute workflow", err)
    }

    log.Printf("Workflow started: ID=%s RunID=%s", we.GetWorkflowID(), we.GetRunID())
    
    // 可选：同步等待结果 (不推荐在 HTTP 请求中长等待)
    // var result string
    // we.Get(context.Background(), &result)
    // we.IsReady()
}
```

---

# 3. ActivityOptions 详解

`ActivityOptions` 用于控制 Activity 的执行行为，特别是超时和重试策略。

| 字段 | 必须 | 类型 | 说明 |
|------|------|------|------|
| **StartToCloseTimeout** | 是 | `time.Duration` | 单次执行超时。从 Activity Worker 开始处理任务到返回结果的最大时长。适用于：控制单个任务执行的最长时间。 |
| **ScheduleToCloseTimeout** | 是 | `time.Duration` | 总执行超时（含重试）。从任务被调度（Workflow 发起）到最终完成（含所有重试）的最大时长。适用于：限制整个业务操作的总耗时。 |
| **ScheduleToStartTimeout** | 否 | `time.Duration` | 排队超时。从任务被调度到被 Worker 拉取的最大时长。适用于：如果 Worker 繁忙或宕机，不希望任务无限期等待。 |
| **HeartbeatTimeout** | 否 | `time.Duration` | 心跳超时。Activity 必须在此间隔内调用 `activity.RecordHeartbeat`，否则视为 Worker 死亡。适用于：长运行任务（如视频转码、大文件处理），用于快速检测 Worker 故障。 |
| **RetryPolicy** | 否 | `RetryPolicy` | 重试策略。默认情况下，Activity 会无限重试（直到超时）。 |
| **TaskQueueName** | 否 | `string` | 指定 Activity 执行的任务队列（默认为 Workflow 的队列）。 |
| **WaitForCancellation** | 否 | `bool` | 如果 Workflow 取消，是否等待 Activity 完成清理工作（默认 false）。 |

> **注意**：`StartToCloseTimeout` 和 `ScheduleToCloseTimeout` 中必须至少设置一个。通常建议设置 `StartToCloseTimeout`。

## 3.1 超时配置最佳实践

### 常规短任务（如 DB 查询）

- 设置 `StartToCloseTimeout`（如 5秒）
- 通常不需要 `HeartbeatTimeout`

### 长运行任务（如 1小时的数据处理）

- 设置 `StartToCloseTimeout` 为稍大于 1小时
- 必须设置 `HeartbeatTimeout`（如 1分钟），并在 Activity 代码中定期调用 `activity.RecordHeartbeat(ctx)`
- 这样如果 Worker 在执行第 30 分钟时崩溃，Server 会在 1 分钟后发现并重新调度，而不是等到 1 小时后

## 3.2 RetryPolicy 配置

```go
RetryPolicy: &temporal.RetryPolicy{
    InitialInterval:    1 * time.Second,  // 首次重试间隔
    BackoffCoefficient: 2.0,              // 指数退避系数 (1s -> 2s -> 4s...)
    MaximumInterval:    100 * time.Second, // 最大重试间隔
    MaximumAttempts:    3,                // 最大尝试次数 (0 为无限)
    NonRetriableErrorTypes: []string{     // 不重试的错误类型
        "InvalidOrderError",              // 如业务逻辑错误
    },
}
```

---

# 4. 最佳实践 (Best Practices)

## 4.1 确定性约束 (Determinism)

**禁止使用的操作：**

| 禁止操作 | 替代方案 | 原因 |
|----------|----------|------|
| `time.Now()` | `workflow.Now(ctx)` | Temporal 通过记录事件历史（Event History）并重新执行 Workflow 代码来恢复状态。如果代码逻辑不确定，重放时会偏离历史记录，导致 `NonDeterministicError`。 |
| `time.Sleep()` | `workflow.Sleep(ctx, duration)` | 同上 |
| `go func()` | `workflow.Go(ctx, func)` | 同上 |
| 随机数 | `workflow.SideEffect` 或确定性随机数 | 同上 |

## 4.2 Activity 设计

- Activity 应该包含所有与外部系统（DB, RPC）的交互
- Activity 默认会无限重试（直到超时），因此必须保证幂等性

## 4.3 版本控制 (Versioning)

如果需要修改正在运行的 Workflow 逻辑，不能直接改代码，必须使用 `workflow.GetVersion` 或 Patch API，否则旧的历史事件在新代码上重放会失败。

## 4.4 数据传输

参数和返回值默认使用 JSON 序列化（Payload Converter）。确保传递的数据是可序列化的。

## SideEffect vs Activity 对比

| 特性 | SideEffect | Activity |
|------|------------|----------|
| **适用场景** | 轻量级、快速的非确定性逻辑。 | 重量级、耗时、易失败的逻辑。 |
| **典型例子** | 生成 UUID、获取随机数、读取某些配置标志。 | 读写数据库、调用第三方 API、文件处理。 |
| **开销** | 极低。不需要调度到 Task Queue，直接在当前 Worker 线程执行。 | 较高。涉及网络传输、序列化、调度等待。 |
| **重试机制** | 无重试。如果函数 panic，Workflow 任务直接失败。 | 有重试。可以配置复杂的 Retry Policy。 |
| **超时控制** | 无。必须瞬间完成。 | 有。可以配置 `StartToCloseTimeout` 等。 |

---

# 5. Core Concepts

## 5.1 Workflow：定义、参数、返回值与类型名

### 5.1.1 基本 Workflow 定义

在 Go SDK 模型中，Workflow Definition 是一个可导出函数：

```go
package yourapp

import (
    "go.temporal.io/sdk/workflow"
)

func YourSimpleWorkflowDefinition(ctx workflow.Context) error {
    return nil
}
```

### 5.4.2 Workflow 参数规范

Workflow 可以有任意数量参数，但强烈推荐用单一结构体对象作为参数，便于后续字段演进而不破坏签名。

**参数要求：**

- 所有参数必须可序列化
- 第一个参数必须是 `workflow.Context`
- 不能使用：channels、functions、variadic、unsafe pointers

**示例：**

```go
type YourWorkflowParam struct {
    WorkflowParamX string
    WorkflowParamY int
}

type YourWorkflowResultObject struct {
    WFResultFieldX string
    WFResultFieldY int
}

func YourWorkflowDefinition(ctx workflow.Context, param YourWorkflowParam) (*YourWorkflowResultObject, error) {
    return &YourWorkflowResultObject{}, nil
}
```

### 5.4.3 Workflow 返回值规范

- 返回值必须可序列化
- Go Workflow 可以返回 `error` 或 `(<custom>, error)`；但调用方只会收到"结果或错误"之一（错误非 nil 时结果会被忽略）

### 5.4.4 自定义 Workflow Type 名称

默认 Workflow Type 名称 = 函数名。注册时可设置 `workflow.RegisterOptions.Name`：

```go
registerWFOptions := workflow.RegisterOptions{
    Name: "JustAnotherWorkflow",
}
yourWorker.RegisterWorkflowWithOptions(YourSimpleWorkflowDefinition, registerWFOptions)
```

## 5.5 Workflow 逻辑：确定性限制与等价 API

Workflow 逻辑必须满足确定性。不能直接做：

- 对 map 使用 range（迭代顺序随机）；可改为 key 收集并排序后再迭代，或用 SideEffect / Activity 来处理
- 直接进行外部交互（外部 API、文件 I/O、调用其他服务等），应放入 Activity

**Go SDK 提供的替代 API：**

| 原生 API | Temporal 替代 API |
|----------|-------------------|
| `time.Now()` | `workflow.Now()` |
| `time.Sleep()` | `workflow.Sleep()` |
| 日志输出 | `workflow.GetLogger()`：确保 replay 时不会重复输出日志 |
| `go` 关键字 | `workflow.Go()` |
| `chan` | `workflow.Channel`（支持 buffered/unbuffered） |
| `select` | `workflow.Selector` |
| `context.Context` | `workflow.Context`（Done() 返回 workflow.Channel） |

## 5.6 Activity：定义、参数、返回值与类型名

### 5.6.1 Activity Definition 形式与依赖复用

Go SDK 中 Activity Definition 可以是：

- 可导出函数
- 或 struct 的导出方法

**Activity 使用 struct 方法的典型价值（共享/复用昂贵资源）：**

- 应用级 DB pool
- 下游服务 client 连接
- 可复用工具/缓存
- 任何希望"进程内初始化一次"的资源

**示例：**

```go
package yourapp

import (
    "context"
)

func YourSimpleActivityDefinition(ctx context.Context) error {
    return nil
}

type YourActivityObject struct {
    Message *string
    Number  *int
}

type YourActivityParam struct {
    ActivityParamX string
    ActivityParamY int
}

type YourActivityResultObject struct {
    ResultFieldX string
    ResultFieldY int
}

func (a *YourActivityObject) YourActivityDefinition(ctx context.Context, param YourActivityParam) (*YourActivityResultObject, error) {
    return &YourActivityResultObject{
        ResultFieldX: "Success",
        ResultFieldY: 1,
    }, nil
}
```

### 5.6.2 Activity 参数与 payload 限制

**明确的 payload 限制与性能提醒：**

- 单个参数最大 **2MB**
- 单次 gRPC message（含所有参数）最大 **4MB**
- 参数与返回值会写入 Workflow Event History；Event History 过大可能影响 Worker 性能（Workflow Task 恢复时会传输整个 history）
- 推荐用单一结构体作为参数，便于字段演进

### 5.6.3 自定义 Activity Type 名称

注册时可设置 `activity.RegisterOptions.Name`：

```go
registerAOptions := activity.RegisterOptions{
    Name: "JustAnotherActivity",
}
yourWorker.RegisterActivityWithOptions(YourSimpleActivityDefinition, registerAOptions)
```

## 5.7 在 Workflow 中启动 Activity

Activity 必须在 Workflow 内启动，调用 `workflow.ExecuteActivity()`：

- 该调用会生成 `ScheduleActivityTask` command，并在 history 中形成 Activity 相关事件（Scheduled/Started/Closed）
- Activity 实现应幂等（可能重试）
- 传入的参数与返回值会被记录到 history；注意数据量
- `ExecuteActivity` 返回 `workflow.Future`，可 `Get()` 获取结果或 `IsReady()` 检查是否完成

**示例：**

```go
activityOptions := workflow.ActivityOptions{
    StartToCloseTimeout: 10 * time.Second, // 或 ScheduleToCloseTimeout
}
ctx := workflow.WithActivityOptions(ctx, activityOptions)

activityParam := YourActivityParam{
    ActivityParamX: param.WorkflowParamX,
    ActivityParamY: param.WorkflowParamY,
}

var a *YourActivityObject
var activityResult YourActivityResultObject
err := workflow.ExecuteActivity(ctx, a.YourActivityDefinition, activityParam).Get(ctx, &activityResult)
if err != nil {
    return nil, err
}
```

## 5.8 Activity Timeouts：必填规则与字段语义

Activity 必须设置以下两者之一：

- `ScheduleToCloseTimeout`
- `StartToCloseTimeout`

设置方式：构造 `workflow.ActivityOptions` 并通过 `workflow.WithActivityOptions()` 绑定到 `workflow.Context`。

**可用超时字段：**

- `StartToCloseTimeout`
- `ScheduleToCloseTimeout`
- `ScheduleToStartTimeout`

**默认值要点：**

| 字段 | 默认值 |
|------|--------|
| `ScheduleToCloseTimeout` | ∞ |
| `ScheduleToStartTimeout` | ∞ |
| `StartToCloseTimeout` | 与 `ScheduleToCloseTimeout` 相同 |
| `WaitForCancellation` | `false` |
| `TaskQueueName` | 继承 Workflow 的 Task Queue |

**字段使用示例：**

```go
activityOptions := workflow.ActivityOptions{
    ActivityID:            "your-activity-id",
    TaskQueueName:         "your-task-queue-name",
    ScheduleToCloseTimeout: 10 * time.Second,
    ScheduleToStartTimeout: 10 * time.Second,
    StartToCloseTimeout:    10 * time.Second,
    HeartbeatTimeout:       10 * time.Second,
    WaitForCancellation:    false,
    OriginalTaskQueueName:  "your-original-task-queue-name",
}
ctx = workflow.WithActivityOptions(ctx, activityOptions)
```

**RetryPolicy 示例：**

```go
retryPolicy := &temporal.RetryPolicy{
    InitialInterval:    time.Second,
    BackoffCoefficient: 2.0,
    MaximumInterval:    time.Second * 100, // 100 * InitialInterval
    MaximumAttempts:    0,                 // unlimited
    NonRetryableErrorTypes: []string{},
}
activityOptions := workflow.ActivityOptions{
    RetryPolicy: retryPolicy,
}
ctx = workflow.WithActivityOptions(ctx, activityOptions)
```

## 5.9 Worker：创建、运行、注册与约束

**创建 Worker：**

- `worker.New(client, taskQueue, worker.Options{})`
- 注册 Workflows：`RegisterWorkflow` / `RegisterWorkflowWithOptions`
- 注册 Activities：`RegisterActivity` / `RegisterActivityWithOptions`
- 运行：`Run(worker.InterruptCh())` 或 `Start()` / `Stop()`

> **注意**：Client 是 heavyweight 对象，建议每个进程只创建一次并复用。

**重要约束：**

所有监听同一个 Task Queue 的 Worker 必须注册处理完全相同的 Workflow Types 与 Activity Types。

- 如果 Worker 拉到一个它不认识的类型，会导致该 Task 失败
- Task 失败不会直接导致对应 Workflow Execution 失败

**注册多个类型：**

注册多个类型时，只需要多次注册，但要确保类型名唯一：

```go
w.RegisterActivity(ActivityA)
w.RegisterActivity(ActivityB)
w.RegisterWorkflow(WorkflowA)
w.RegisterWorkflow(WorkflowB)
```

## 5.10 Dynamic Workflow / Dynamic Activity

动态 Workflow / Activity 是一种兜底机制：当 Worker 上没有注册与调用名称匹配的 Workflow/Activity 时，会走动态处理器。

- `worker.RegisterDynamicWorkflow()`：每个 Worker 只能注册一个
- `worker.RegisterDynamicActivity()`：每个 Worker 只能注册一个
- 动态定义必须接收 `converter.EncodedValues` 并自行解码参数

**示例：**

```go
func DynamicWorkflow(ctx workflow.Context, args converter.EncodedValues) (string, error) {
    var arg1, arg2 string
    if err := args.Get(&arg1, &arg2); err != nil {
        return "", err
    }
    info := workflow.GetInfo(ctx)
    return fmt.Sprintf("%s - %s - %s", info.WorkflowType.Name, arg1, arg2), nil
}

func DynamicActivity(ctx context.Context, args converter.EncodedValues) (string, error) {
    var arg1, arg2 string
    if err := args.Get(&arg1, &arg2); err != nil {
        return "", err
    }
    info := activity.GetInfo(ctx)
    return fmt.Sprintf("%s - %s - %s", info.WorkflowType.Name, arg1, arg2), nil
}
```

## 5.11 Multithreaded

Temporal Go SDK 针对 Workflow 代码的并发有强制约束，以确保确定性和可重放性：

| 禁止使用 | 应使用 |
|----------|--------|
| 原生 `go` 启动 goroutine | `workflow.Go(ctx, func)` |
| 原生 `chan` 与 `select` | `workflow.Channel` 与 `workflow.Selector` |
| 原生 `time.Sleep()` | `workflow.Sleep()` |

> **说明**：Workflow 内的并发本质是由 Temporal 的调度器与事件历史驱动的"协作式并发"，而不是任意 goroutine。

**推荐原则：**

- 将外部并发/异步交互放入 Activity 里执行
- Workflow 中需要并发时，使用 `workflow.Go` 配合 Future/Selector
- 避免使用任意非确定性操作（随机数、map range 等）

## 5.12 Cancellation

取消包含两条路径：Workflow 端处理取消 + Activity 端响应取消。

### 5.12.1 Workflow 内处理 Cancellation

- Workflow 可以在收到取消后执行清理逻辑
- 使用 `workflow.NewDisconnectedContext` 在取消后执行"清理 Activity"
- 是否最终返回 Canceled 状态由业务决定：可以返回取消错误或正常完成

**示例：**

```go
func YourWorkflow(ctx workflow.Context) error {
    activityOptions := workflow.ActivityOptions{
        HeartbeatTimeout:    5 * time.Second,
        WaitForCancellation: true,
    }
    ctx = workflow.WithActivityOptions(ctx, activityOptions)

    defer func() {
        if !errors.Is(ctx.Err(), workflow.ErrCanceled) {
            return
        }
        newCtx, _ := workflow.NewDisconnectedContext(ctx)
        _ = workflow.ExecuteActivity(newCtx, a.CleanupActivity).Get(ctx, nil)
    }()

    err := workflow.ExecuteActivity(ctx, a.ActivityToBeCanceled).Get(ctx, nil)
    if err != nil {
        return err
    }
    return nil
}
```

### 5.12.2 Activity 内处理 Cancellation

Activity 要"感知取消"，必须 Heartbeat：

- Heartbeat 会携带取消信号
- 若 Activity 不 Heartbeat，就无法及时收到取消

**常见模式：**

```go
func (a *Activities) ActivityToBeCanceled(ctx context.Context) (string, error) {
    for {
        select {
        case <-time.After(1 * time.Second):
            activity.RecordHeartbeat(ctx, "")
        case <-ctx.Done():
            return "canceled", nil
        }
    }
}
```

### 5.12.3 发起取消请求

通过 Client 调用 `CancelWorkflow`：

```go
err := temporalClient.CancelWorkflow(context.Background(), workflowID, runID)
```

### 5.12.4 取消后的 Heartbeat

Activity 在取消后仍可 Heartbeat：

- 会记录 warning 日志
- 但 heartbeat 仍会被发送
- 可用于最后一次进度落盘或清理

### 5.12.5 Reset Workflow Execution

Reset 用于从 Event History 的某个点"回滚并重跑"：

- 当前 Execution 被终止
- 从指定历史点启动一个新的 Execution
- 适用于修复 non-deterministic 问题或阻塞的 Workflow


### 5.12.5 Reset Workflow Execution

Reset 用于从 Event History 的某个点"回滚并重跑"：

- 当前 Execution 被终止
- 从指定历史点启动一个新的 Execution
- 适用于修复 non-deterministic 问题或阻塞的 Workflow

---

## 5.13 Versioning

Temporal Workflow 可能运行数月甚至数年，代码变更必须保证确定性重放。官方提供两类 Versioning 方法：

### 5.13.1 Worker Versioning

通过给 Worker 打版本标记并渐进式发布：

- 老 Worker 运行旧代码
- 新 Worker 运行新代码
- Workflow 可固定到特定版本
- 可避免大量 Patch 分支

### 5.13.2 Patching（GetVersion）

使用 `workflow.GetVersion` 引入"分支化代码路径"，确保老 Workflow 走旧逻辑，新 Workflow 走新逻辑。

**示例：**

```go
v := workflow.GetVersion(ctx, "Step1", workflow.DefaultVersion, 1)
if v == workflow.DefaultVersion {
    _ = workflow.ExecuteActivity(ctx, ActivityA, data).Get(ctx, &result)
} else {
    _ = workflow.ExecuteActivity(ctx, ActivityC, data).Get(ctx, &result)
}
```

**升级到版本 2 后：**

```go
v := workflow.GetVersion(ctx, "Step1", workflow.DefaultVersion, 2)
if v == workflow.DefaultVersion {
    _ = workflow.ExecuteActivity(ctx, ActivityA, data).Get(ctx, &result)
} else if v == 1 {
    _ = workflow.ExecuteActivity(ctx, ActivityC, data).Get(ctx, &result)
} else {
    _ = workflow.ExecuteActivity(ctx, ActivityD, data).Get(ctx, &result)
}
```

当旧版本 Workflow 全部离开 retention 之后，可以逐步移除旧分支，并保持第一处 GetVersion 调用以支持未来演进。

### 5.13.3 Workflow Cutover（改名切换）

一种替代方案是复制 Workflow 定义并改名（例如 Workflow → WorkflowV2），并同时注册旧/新版本：

```go
w.RegisterWorkflow(PizzaWorkflow)
w.RegisterWorkflow(PizzaWorkflowV2)
```

这可以避免 Patch 分支，但无法为旧的运行中 Workflow 版本化，且代码重复。

---

## 5.14 Failure Detection

### 5.14.1 Workflow Timeouts

Workflow Timeout 在启动 Workflow 时设置（`StartWorkflowOptions`）：

- **WorkflowExecutionTimeout**：整个 Workflow Execution 最大时长
- **WorkflowRunTimeout**：单次 Run 最大时长
- **WorkflowTaskTimeout**：单次 Workflow Task 处理时长

> **注意**：官方不建议随意设置 Workflow Timeout，因为 Workflow 天生可以长时间运行且可恢复；若只是延迟执行建议用 Timer。

**示例：**

```go
workflowOptions := client.StartWorkflowOptions{
    WorkflowExecutionTimeout: 24 * 365 * 10 * time.Hour,
    WorkflowRunTimeout:       24 * 365 * 10 * time.Hour,
    WorkflowTaskTimeout:      10 * time.Second,
}
```

### 5.14.2 Workflow Retry Policy

Workflow 默认不自动重试。若业务需要，可在 `StartWorkflowOptions` 设置 `RetryPolicy`：

```go
retrypolicy := &temporal.RetryPolicy{
    InitialInterval:    time.Second,
    BackoffCoefficient: 2.0,
    MaximumInterval:    100 * time.Second,
}
workflowOptions := client.StartWorkflowOptions{
    RetryPolicy: retrypolicy,
}
```

### 5.14.3 Activity Timeouts

Activity Timeouts 在 `ActivityOptions` 中设置：

- **ScheduleToCloseTimeout**：整体执行时间
- **StartToCloseTimeout**：单次执行时间
- **ScheduleToStartTimeout**：排队等待时间

必须设置 StartToClose 或 ScheduleToClose 之一。

### 5.14.4 Activity Retry Policy

Activity 默认有重试策略，如需自定义可在 `ActivityOptions` 中提供 `RetryPolicy`。

```go
ao := workflow.ActivityOptions{
    StartToCloseTimeout: time.Minute,
    RetryPolicy: &temporal.RetryPolicy{
        InitialInterval:    200 * time.Millisecond,
        BackoffCoefficient: 2.0,
        MaximumInterval:    2 * time.Second,
        MaximumAttempts:    3,
    },
}
```

**Activity 重试 vs Workflow 重试：**

| 特性 | Activity 重试 | Workflow 重试 |
|------|---------------|---------------|
| **作用范围** | 局部。只重试当前这一个步骤（函数）。 | 全局。重试整个业务流程，从第一行代码开始重新执行。 |
| **状态保留** | 保留。Workflow 的上下文、之前已完成的步骤（变量、历史）都还在，只是卡在当前这步等待成功。 | 清空。之前的执行历史（History）会被归档，新的执行会从头开始，所有局部变量重置。 |
| **默认行为** | 默认开启（无限重试）。只要没设置超时，它会一直重试直到成功。 | 默认关闭。如果 Workflow 返回 error，通常直接标记为 Failed。 |
| **适用场景** | 网络超时、第三方 API 报错 (503/500)、数据库锁冲突等短暂性故障。 | 某些极端的环境问题，或者业务要求"如果失败就彻底重新跑一次"的场景（如 Cron 任务）。 |
| **ID 变化** | WorkflowID 和 RunID 都不变。 | WorkflowID 不变，但会生成一个新的 RunID。 |

### 5.14.5 Next Retry Delay（自定义下一次重试间隔）

Activity 可以通过 `temporal.NewApplicationErrorWithOptions` 设置 `NextRetryDelay` 来覆盖重试间隔：

```go
attempt := activity.GetInfo(ctx).Attempt
return temporal.NewApplicationErrorWithOptions(
    fmt.Sprintf("attempt %d failed", attempt),
    "NextDelay",
    temporal.ApplicationErrorOptions{NextRetryDelay: 3 * time.Second * delay},
)
```

---

## 5.15 Sessions

Worker Session 是一个功能，提供简单的 API 用于任务路由，确保 Activity 任务在相同的 Worker 上执行，而无需手动指定任务队列名称。

**例如**，一个包含三个独立 Activity 的工作流：

1. 下载文件（可以在任何 Worker 上执行）
2. 处理文件（必须在下载文件的同一主机上执行）
3. 上传文件到其他位置（必须在同一主机上执行）

**1. 启用 Worker Sessions：** 将 `EnableSessionWorker` 设置为 true。

**2. 更改最大并发会话数：** 限制 Worker 上同时运行的会话数量。`MaxConcurrentSessionExecutionSize: 1000`

```go
func main() {
    // ...
    // Enable Sessions for this Worker.
    workerOptions := worker.Options{
        EnableSessionWorker: true,
        MaxConcurrentSessionExecutionSize: 1000,
    }
    // ...
    w := worker.New(temporalClient, "fileprocessing", workerOptions)
    w.RegisterWorkflow(sessions.SomeFileProcessingWorkflow)
    w.RegisterActivity(&sessions.FileActivities{})
    err = w.Run(worker.InterruptCh())
    // ...
}
```

**3. 创建 Worker Session：** 在工作流代码中创建会话。

```go
sessionOptions := &workflow.SessionOptions{
    CreationTimeout:  time.Minute,
    ExecutionTimeout: time.Minute,
}
sessionCtx, err := workflow.CreateSession(ctx, sessionOptions)
if err != nil {
    return err
}
defer workflow.CompleteSession(sessionCtx)
```

在没有使用 Worker Sessions 的情况下，Activity 会随机分配给监听同一 Task Queue 的任意可用 Worker。而使用 Worker Sessions 时，系统会在 Session 创建时选择一个 Worker，并确保该 Session 中的所有 Activity 都由同一个 Worker 执行，从而提供确定性的任务路由。

**注：**

1. **会话失败检测过于严格**：目前，当 Worker 进程（Worker Process）死亡时，会话（Session）就被认为是失败的。这意味着即使 Worker 主机（Worker host）仍然存活，只是进程重启了，会话也会失败。
2. **资源限制过于简单**：当前实现假设所有会话都消耗相同类型的资源，并且只有一个全局限制。这种设计不够灵活，无法满足不同资源类型的差异化需求。

---

## 5.16 Selector

在普通 Go 程序中，`select` 让 goroutine 可以同时等待多个通信操作（如 channel 的发送/接收）。它会阻塞，直到其中一个 case 可以执行。关键问题：**如果有多个 case 同时就绪，Go 会随机选择一个执行。**

Temporal 工作流必须保证确定性执行：

- 相同的输入必须产生完全相同的执行路径
- 这是为了实现可靠的重放（replay）和容错
- 如果使用 Go 原生的随机选择，重放时可能选择不同的分支，导致不一致

### 5.16.1 Futures

等待异步任务（如 Activity）完成。

```go
// 1. 执行一个 Activity（异步任务）
work := workflow.ExecuteActivity(ctx, ExampleActivity)

// 2. 将 Future 添加到 Selector
selector.AddFuture(work, func(f workflow.Future) {
    // 当 Activity 完成时，这个回调函数会被执行
    // 可以在这里处理结果或错误
})

// 3. 阻塞等待 Future 完成
selector.Select(ctx)
```

**每个 Future 只匹配一次**

- 即使多次调用 `selector.Select(ctx)`，同一个 Future 的回调只会执行一次
- 这意味着你不能重复等待同一个异步任务的结果
- 一旦 Future 完成并被处理，它就从 Selector 中移除

**匹配顺序未定义**

- 当多个 Future 同时就绪时，Selector 处理它们的顺序是不确定的
- 这与 Go 原生 select 的随机性类似，但 Temporal 保证了确定性重放
- 重放时，会按照第一次执行时的实际顺序来处理，确保一致性

### 5.16.2 Timers

- **硬超时**：任务超时后直接取消或失败，可能触发重试
- **软超时**：任务超时后不取消任务本身，而是执行一些辅助操作（如发送通知）。任务继续执行，直到自然完成或失败

```go
var processingDone bool

// 1. 启动订单处理 Activity（核心任务）
f := workflow.ExecuteActivity(ctx, OrderProcessingActivity)
selector.AddFuture(f, func(f workflow.Future) {
    processingDone = true  // 标记任务已完成
    cancelHandler()        // 取消定时器（如果还在等待）
})

// 2. 创建定时器（软超时阈值）
timerFuture := workflow.NewTimer(childCtx, processingTimeThreshold)
selector.AddFuture(timerFuture, func(f workflow.Future) {
    if !processingDone {  // 定时器触发时，检查任务是否完成
        // 任务还未完成，发送延迟通知邮件
        _ = workflow.ExecuteActivity(ctx, SendEmailActivity).Get(ctx, nil)
    }
})

// 3. 等待：要么任务完成，要么定时器触发
selector.Select(ctx)
```

### 5.16.3 Channels

```go
// 1. 获取信号通道
channel := workflow.GetSignalChannel(ctx, channelName)

// 2. 定义接收变量
var signalVal string

// 3. 将通道添加到 Selector
selector.AddReceive(channel, func(c workflow.ReceiveChannel, more bool) {
    // 4. 显式接收消息
    c.Receive(ctx, &signalVal)
    // 5. 处理接收到的信息
    // do something with received information
})
```

---

## 5.17 Schedules

### 5.17.1 创建

```go
func main() {
    // ...
    scheduleID := "schedule_id"
    workflowID := "schedule_workflow_id"
    // Create the schedule.
    scheduleHandle, err := temporalClient.ScheduleClient().Create(ctx, client.ScheduleOptions{
        ID:   scheduleID,
        Spec: client.ScheduleSpec{
            // 每天上午 10 点执行
            Calendars: []client.ScheduleCalendarSpec{
                {
                    Hour: []client.ScheduleRange{{Start: 10}},
                },
            },
            // 或者使用 cron 表达式
            CronExpressions: []string{"0 10 * * *"},
        },
        Action: &client.ScheduleWorkflowAction{
            ID:        workflowID,
            Workflow:  schedule.ScheduleWorkflow,
            TaskQueue: "schedule",
        },
    })
    // ...
}
// ...
```

### 5.17.2 回填（Backfill）

在调度规定的时间范围之外，提前执行工作流任务。主要用于：

1. 执行错过的或延迟的操作
2. 在正式调度前测试工作流
3. 补全历史数据

```go
// 回填配置
client.ScheduleBackfillOptions{
    Backfill: []client.ScheduleBackfill{
        {
            Start:   now.Add(-4 * time.Minute),  // 回填开始时间：4 分钟前
            End:     now.Add(-2 * time.Minute),  // 回填结束时间：2 分钟前
            Overlap: enums.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL,
        },
        {
            Start:   now.Add(-2 * time.Minute),  // 第二个回填区间：2 分钟前到现在
            End:     now,
            Overlap: enums.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL,
        },
    },
}
// 执行回填
err = scheduleHandle.Backfill(ctx, client.ScheduleBackfillOptions{...})
```

### 5.17.3 暂停

**暂停调度：** 临时停止调度的所有未来工作流执行，但不会删除调度或影响已启动的工作流。

**恢复调度：** 重新启用已暂停的调度，使其按照原定计划继续执行工作流。

```go
func main() {
    // ...
    err = scheduleHandle.Pause(ctx, client.SchedulePauseOptions{
        Note: "The Schedule has been paused.",
    })
    // ...
    err = scheduleHandle.Unpause(ctx, client.ScheduleUnpauseOptions{
        Note: "The Schedule has been unpaused.",
    })
}
```

### 5.17.4 触发（Trigger）

**触发调度：** 立即执行调度中定义的工作流动作，不受原定时间规则限制。这相当于手动"立即执行"按钮。

```go
func main() {
    // ...
    for i := 0; i < 5; i++ {
        scheduleHandle.Trigger(ctx, client.ScheduleTriggerOptions{
            Overlap: enums.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL,
        })
        time.Sleep(2 * time.Second)
    }
}
```

**Overlap：重叠策略，决定当有工作流正在运行时如何处理新的触发**

- `SCHEDULE_OVERLAP_POLICY_ALLOW_ALL`：允许所有重叠执行
- `SCHEDULE_OVERLAP_POLICY_SKIP`：跳过重叠的执行
- `SCHEDULE_OVERLAP_POLICY_BUFFER_ONE`：缓冲一个执行
- `SCHEDULE_OVERLAP_POLICY_CANCEL_OTHER`：取消其他执行

### 5.17.5 延迟执行（Delay）

Start Delay 用于在特定的一次性未来时间点调度工作流执行，而不是使用重复的调度计划。它确定在启动工作流执行之前需要等待的时间量。

**特点：**

1. **一次性调度**：与重复执行的 Schedule 和 Cron Jobs 不同，Start Delay 适用于只需要在未来某个时间点执行一次的工作流。
2. **信号中断机制：**
   - 如果在延迟期间工作流收到 Signal-With-Start 或 Update-With-Start 信号，会立即分派工作流任务，剩余延迟将被绕过。
   - 如果收到非 Signal-With-Start 的信号，延迟不会被中断，工作流会继续延迟直到延迟到期或收到 Signal-With-Start 信号。
3. **兼容性限制**：Start Delay 工作流执行与 Schedule 和 Cron Jobs 不兼容。

```go
workflowOptions := client.StartWorkflowOptions{
    // ...
    // 12 小时后启动工作流
    StartDelay: time.Hours * 12,
    // ...
}
workflowRun, err := c.ExecuteWorkflow(context.Background(), workflowOptions, YourWorkflowDefinition)
if err != nil {
    // ...
}
```

在 Start Delay 功能出现之前，开发者通常使用 `Workflow.sleep` 在工作流开始时延迟执行。两者的主要区别是：

- **Start Delay**：在工作流开始执行之前就设置延迟，不会占用工作流执行资源
- **Workflow.sleep**：在工作流内部执行延迟，会占用工作流执行线程

---

## 5.18 Namespace

Namespace 是 Temporal 中的一个逻辑隔离单元，用于将 Workflow Execution（工作流执行）按照特定需求进行分组和隔离。主要用于：

1. **环境隔离**：例如，创建 dev（开发）、staging（预发布）、prod（生产）等不同的命名空间
2. **团队隔离**：确保不同团队的工作流互不影响，比如 teamA 和 teamB 使用不同的命名空间
3. **资源管理**：每个命名空间可以有独立的配置和资源限制

具体设定及管理方法见：[Temporal Client - Go SDK | Temporal Platform Documentation](https://docs.temporal.io/develop/go)

---

## 5.19 Messages

Temporal 中的 Workflow 可以被看作是一个有状态的 Web 服务，它能够接收和处理外部消息。

**Workflow 可以接收三种类型的消息：**

- **Queries（查询）**：只读请求，用于读取 Workflow 的当前状态，但不会阻塞 Workflow 的执行
  - 同步：不会在 Workflow 事件历史中添加条目
  - 适用于获取 Workflow 状态信息
- **Signals（信号）**：异步写请求，用于改变正在运行的 Workflow 的状态
  - 异步：发送后不等待响应
  - 不会阻塞 Workflow 执行
  - 通过 Signal 通道接收
- **Updates（更新）**：同步的、可追踪的写请求
  - 同步：必须等待 Worker 确认请求
  - 会在 Workflow 事件历史中添加条目
  - 适用于需要同步响应的读写操作

### 5.19.1 查询（Query）

允许外部客户端查询工作流的当前状态，比如查询工作流执行到哪个阶段、已完成的活动数量等。Query Handler 的代码必须非常快且非阻塞。**严禁在 Query Handler 里执行 Activity、Sleep、await 或发起网络请求**。它只能做简单的内存读取和计算。

**服务端：**

```go
func MyWorkflow(ctx workflow.Context) error {
    // 1. 定义一个内部状态变量
    currentState := "Started"
    itemsProcessed := 0

    // 2. 注册 Query Handler
    // 外部可以通过 "current_state" 这个名字来查询 currentState 的值
    err := workflow.SetQueryHandler(ctx, "current_state", func() (string, error) {
        return currentState, nil
    })
    
    // 外部可以通过 "progress" 来查询处理数量
    err = workflow.SetQueryHandler(ctx, "progress", func() (int, error) {
        return itemsProcessed, nil
    })

    // 3. 业务逻辑（会不断更新状态）
    currentState = "Running"
    for i := 0; i < 10; i++ {
        // 模拟耗时操作
        workflow.Sleep(ctx, time.Second)
        itemsProcessed++
    }
    
    currentState = "Completed"
    return nil
}
```

**客户端：**

```go
// 发起查询
resp, err := client.QueryWorkflow(ctx, workflowID, runID, "current_state")
var state string
resp.Get(&state)
fmt.Println("当前状态:", state) // 输出：Running
```

### 5.19.2 信号（Signal）

Signal 是一种异步消息，用于向正在运行的 Workflow Execution（工作流执行）发送指令，从而改变其状态或控制其流程。它类似于事件驱动的编程模型。

- **重要**：SDK 会在没有创建对应通道时缓冲信号
- 这意味着你可以在工作流初始化完成后再创建信号通道，不会丢失信号
- 但需要在工作流完成前排空（drain）所有缓冲的信号

**阻塞：**

```go
func MyWorkflow(ctx workflow.Context) error {
    var signalData string
    
    // 1. 获取 Signal Channel
    // "my-signal-name" 是信号的名称，客户端发送时必须匹配
    signalChan := workflow.GetSignalChannel(ctx, "my-signal-name")

    // 2. 阻塞等待接收数据
    // Receive 会一直挂起，直到收到信号
    signalChan.Receive(ctx, &signalData)

    workflow.GetLogger(ctx).Info("收到信号", "data", signalData)
    
    // 继续执行后续逻辑...
    return nil
}
```

**非阻塞：**

```go
func MyWorkflow(ctx workflow.Context) error {
    signalChan := workflow.GetSignalChannel(ctx, "update-config")
    timerFuture := workflow.NewTimer(ctx, time.Hour) // 1 小时超时
    
    var configData string
    selector := workflow.NewSelector(ctx)

    // 1. 注册 Signal 处理逻辑
    selector.AddReceive(signalChan, func(c workflow.ReceiveChannel, more bool) {
        c.Receive(ctx, &configData)
        workflow.GetLogger(ctx).Info("收到配置更新", "config", configData)
    })

    // 2. 注册 Timer 处理逻辑
    selector.AddFuture(timerFuture, func(f workflow.Future) {
        workflow.GetLogger(ctx).Info("超时了，没有收到信号")
    })

    // 3. 开始等待 (只会触发其中一个)
    selector.Select(ctx)

    return nil
}
```

**循环接受：**

```go
func OrderWorkflow(ctx workflow.Context) error {
    signalChan := workflow.GetSignalChannel(ctx, "modify-order")
    selector := workflow.NewSelector(ctx)

    // 注册监听
    selector.AddReceive(signalChan, func(c workflow.ReceiveChannel, more bool) {
        var action string
        c.Receive(ctx, &action)
        workflow.GetLogger(ctx).Info("处理订单修改", "action", action)
    })

    // 持续监听循环
    for {
        // 阻塞等待信号
        selector.Select(ctx)
        
        // 检查是否满足退出条件
        if isOrderCompleted {
            break
        }
    }
    return nil
}
```

**客户端发送：**

```go
// 在 Client 端代码中
err := client.SignalWorkflow(ctx, "workflow-id", "", "my-signal-name", "有些数据")
```

### 5.19.3 更新（Update）

Update 是一种可追踪的同步请求，发送给正在运行的 Workflow Execution（工作流执行）。它具有以下特点：

- **同步性**：发送方必须等待 Worker 接受或拒绝该 Update。Update 支持定义一个"验证器"。在 Update 被写入历史记录之前，验证器会先运行。如果验证失败，请求会被直接拒绝，不会污染 Workflow 的历史记录。
- **可返回值**：可以返回结果或异常
- **可追踪**：所有 Update 操作都会记录在 Event History 中

**服务端：**

```go
import (
    "go.temporal.io/sdk/workflow"
)

// 定义 Update 的输入参数
type UpdateInput struct {
    Amount int
}

// Workflow 定义
func MyWorkflow(ctx workflow.Context) error {
    counter := 0

    // 1. 定义 Update Handler (处理逻辑)
    // 注意：第一个参数必须是 workflow.Context
    updateHandler := func(ctx workflow.Context, input UpdateInput) (int, error) {
        // 这里可以执行任何 Workflow 逻辑，包括 Activity
        // 模拟耗时操作
        if err := workflow.Sleep(ctx, time.Second); err != nil {
            return 0, err
        }
        
        counter += input.Amount
        return counter, nil // 返回更新后的值
    }

    // 2. 定义 Validator (验证逻辑 - 可选)
    // 验证器不能修改状态，不能包含阻塞操作
    validator := func(ctx workflow.Context, input UpdateInput) error {
        if input.Amount <= 0 {
            return fmt.Errorf("amount must be positive")
        }
        return nil
    }

    // 3. 注册 Update Handler
    // "add_amount" 是 Update 的名称
    if err := workflow.SetUpdateHandlerWithOptions(ctx, "add_amount", updateHandler, workflow.UpdateHandlerOptions{
        Validator: validator,
    }); err != nil {
        return err
    }

    // 阻塞 Workflow，防止直接退出
    // 在实际业务中，这里通常是 select {} 或者等待某个退出信号
    workflow.Await(ctx, func() bool { return false })
    return nil
}
```

**客户端：**

```go
import (
    "context"
    "fmt"
    "go.temporal.io/sdk/client"
)

func main() {
    // ... 初始化 client ...

    // 准备输入
    input := UpdateInput{Amount: 10}

    // 发起 Update 请求
    // UpdateWorkflow 是同步阻塞的，直到 Handler 执行完毕返回结果
    updateHandle, err := c.UpdateWorkflow(context.Background(), client.UpdateWorkflowOptions{
        WorkflowID:   "my-workflow-id",
        RunID:        "", // 使用最新的 RunID
        UpdateName:   "add_amount",
        Args:         []interface{}{input},
        WaitForStage: client.WorkflowUpdateStageCompleted, // 等待直到 Update 完成
    })

    if err != nil {
        // 如果 Validator 拒绝，或者 Workflow 不存在，这里会报错
        panic(err)
    }

    // 获取结果
    var result int
    if err := updateHandle.Get(context.Background(), &result); err != nil {
        panic(err)
    }

    fmt.Printf("Update 成功，当前 Counter 值：%d\n", result)
}
```

---

## 5.20 Child Workflows

Child Workflow（子工作流）是指在一个 Workflow（父工作流）的执行过程中，启动并调用的另一个 Workflow。

**使用 Child Workflow 主要有以下几个好处：**

- **代码复用与模块化**：如果你有一个复杂的逻辑（比如"处理退款"），它可能包含多个 Activity 和分支判断。你可以把它封装成一个独立的 Workflow，然后在"订单取消"、"用户投诉"等不同的父 Workflow 中重复调用它。
- **突破历史记录限制 (History Size Limit)**：Temporal 对单个 Workflow 的 Event 数量有限制（建议不超过 5 万条）。如果你有一个超长流程（比如处理 100 万条数据），直接在一个 Workflow 里循环做会导致历史记录爆炸。解决方案：把处理逻辑拆分到 Child Workflow 中。父 Workflow 只负责分发任务，每个 Child Workflow 有自己独立的历史记录，互不影响。
- **独立运维与隔离**：Child Workflow 可以运行在不同的 Task Queue 上，甚至由不同的团队维护。这有助于实现微服务架构下的逻辑解耦。
- **并行执行**：父 Workflow 可以同时启动 10 个 Child Workflow 并行工作，然后等待它们全部完成（Fan-out / Fan-in 模式）。

### 5.20.1 父子关系与生命周期

- **默认关联**：默认情况下，Child Workflow 的生命周期受 Parent Workflow 控制。
  - 如果 Parent 终止（Terminate），Child 也会被终止。
  - 如果 Parent 完成，Child 不受影响（除非显式配置了 ParentClosePolicy）。
- **ParentClosePolicy**：你可以配置当父 Workflow 关闭（完成、失败或超时）时，子 Workflow 该怎么办：
  - **TERMINATE (默认)**：父死子亡。
  - **ABANDON**：父死子继（子 Workflow 继续运行，变成"孤儿"）。
  - **REQUEST_CANCEL**：父关闭时，向子发送取消请求。

### 5.20.2 Fire-and-Forget

**场景描述**：父工作流只负责把子工作流启动起来，然后父工作流就直接结束了，根本不关心子工作流的结果（也不等待它做完）。

**关键点**：必须设置 `ParentClosePolicy` 为 `ABANDON`。

- 默认情况下，如果父工作流结束（Completed），它会把所有还在运行的子工作流杀掉（Terminate）。
- 设置为 ABANDON 后，父工作流结束时，子工作流会变成"孤儿"继续独立运行。

```go
func ParentWorkflow(ctx workflow.Context) error {
    // 1. 关键配置：父死子继 (ABANDON)
    cwo := workflow.ChildWorkflowOptions{
        WorkflowID:        "async-child-123",
        ParentClosePolicy: enums.PARENT_CLOSE_POLICY_ABANDON, // 重要！
    }
    ctx = workflow.WithChildWorkflowOptions(ctx, cwo)

    // 2. 启动子工作流
    // 这行代码只是发送了一个启动指令，几乎瞬间完成
    future := workflow.ExecuteChildWorkflow(ctx, MyChildWorkflow, "有些参数")

    // 3. (可选) 等待子工作流"启动成功"
    // 只要 Temporal Server 确认收到了启动请求，这里就会返回
    // 这不是等待"执行完成"，而是等待"成功创建"
    var childExecution workflow.Execution
    if err := future.GetChildWorkflowExecution().Get(ctx, &childExecution); err != nil {
        return err
    }
    
    workflow.GetLogger(ctx).Info("子工作流已启动，父工作流即将退出", "ChildID", childExecution.ID)

    // 4. 父工作流直接结束
    // 子工作流会在后台继续运行，直到它自己完成
    return nil
}
```

### 5.20.3 并行执行 (Fan-out / Fan-in)

**场景描述**：父工作流同时启动 10 个子工作流（异步启动），让它们并行跑，最后再等待它们全部完成。

```go
func ParallelParentWorkflow(ctx workflow.Context) error {
    var futures []workflow.ChildWorkflowFuture

    // 1. 异步启动 10 个子工作流 (Fan-out)
    for i := 0; i < 10; i++ {
        cwo := workflow.ChildWorkflowOptions{
            WorkflowID: fmt.Sprintf("child-%d", i),
        }
        ctx = workflow.WithChildOptions(ctx, cwo)
        
        // 这里不会阻塞！循环会瞬间跑完，启动 10 个任务
        f := workflow.ExecuteChildWorkflow(ctx, MyChildWorkflow, i)
        futures = append(futures, f)
    }

    // 2. 在这里可以做点别的事...
    workflow.Sleep(ctx, time.Second)

    // 3. 收集结果 (Fan-in)
    for _, f := range futures {
        var result string
        // 这里才会阻塞，等待每个子工作流完成
        if err := f.Get(ctx, &result); err != nil {
            return err
        }
        workflow.GetLogger(ctx).Info("子工作流完成", "result", result)
    }

    return nil
}
```

### 5.20.4 只启动，不等待结果

**场景描述**：你需要确保子工作流已经成功运行起来了（比如确保 WorkflowID 没有冲突，参数没问题），但不需要等待它跑完。

```go
func StartAndWaitWorkflow(ctx workflow.Context) error {
    future := workflow.ExecuteChildWorkflow(ctx, LongRunningChild)

    workflow.GetLogger(ctx).Info("确认子工作流已成功运行", "RunID", execution.RunID)

    // 继续做父工作流自己的事，不再关心子工作流何时结束
    // 注意：如果没有设置 ABANDON 策略，父工作流结束时子工作流会被杀掉
    // 如果希望父工作流一直运行直到某个信号，可以使用 Select
    workflow.Await(ctx, func() bool { return false }) 
    
    return nil
}
```

---

## 5.21 Continue-As-New

Continue-As-New 是 Temporal 中用于解决 Workflow 执行历史（Event History）过大问题的一种核心机制。简单来说，它的作用是："重启"当前的 Workflow，清空所有历史记录，但保留必要的参数，以此开始一个新的轮回。

**其有如下关键特性：**

- **原子性**：旧的结束和新的开始是事务性的，不会出现"旧的断了，新的没起来"的情况。
- **清理历史**：这是唯一能"瘦身"Workflow 历史的方法。
- **ID 保持**：外部系统如果一直用 WorkflowID 来查询状态，是感觉不到变化的（除了 RunID 变了）。

### 5.21.1 背景

Temporal 的 Workflow 状态是基于 Event History 重放恢复的。

- 如果你的 Workflow 是一个无限循环（例如：每分钟执行一次任务，永远不停止）。
- 或者你的 Workflow 处理了海量数据（例如：循环调用了 10 万次 Activity）。

那么，Event History 会变得越来越长（几万、几十万条 Event）。这会导致两个严重问题：

1. **性能下降**：Worker 每次加载这个 Workflow 都要下载并重放几十万条历史记录，CPU 和网络开销巨大。
2. **硬限制**：Temporal Server 对单个 Workflow 的历史记录大小有限制（默认通常是 50MB 或 50,000 条 Event）。一旦超过这个限制，Workflow 就会失败（Terminated）。

### 5.21.2 如何使用

当你调用 `workflow.NewContinueAsNewError` 并返回时：

1. **当前 Workflow 结束**：当前的 Workflow Execution 会以 ContinuedAsNew 状态正常关闭。
2. **新 Workflow 启动**：Temporal 会原子性地（Atomic）立即启动一个新的 Workflow Execution。
   - **WorkflowID 不变**：它依然沿用之前的 WorkflowID。
   - **RunID 改变**：它有一个全新的 RunID。
   - **历史清零**：新的 Workflow 从第一行代码开始执行，历史记录是空的（只有 WorkflowExecutionStarted）。
   - **参数传递**：你可以把当前的一些关键状态（比如 loopCount，cursor）作为参数传给新的 Workflow。

### 5.21.3 代码示例

```go
func LoopingWorkflow(ctx workflow.Context, iteration int) error {
    // 业务逻辑：执行一些任务
    logger := workflow.GetLogger(ctx)
    logger.Info("Current Iteration", "i", iteration)
    
    // 模拟做一些事情
    workflow.Sleep(ctx, time.Minute)

    // 检查是否需要"重启"以清理历史
    // 比如每执行 100 次，或者每天，就 ContinueAsNew 一次
    if iteration >= 100 {
        // 关键代码：返回一个特殊的 Error
        // 这会告诉 Temporal："请结束我，并用新的参数 (0) 重新启动我"
        return workflow.NewContinueAsNewError(ctx, LoopingWorkflow, 0)
    }

    // 如果没达到阈值，就递归调用自己（注意：这在 Temporal 里是不推荐的直接递归，
    // 通常是在一个 for 循环里，或者利用 ContinueAsNew 来实现"伪递归"）
    
    // 更常见的写法是配合 for 循环：
    /*
    for i := 0; i < 100; i++ {
        workflow.Sleep(ctx, time.Minute)
        // do something...
    }
    return workflow.NewContinueAsNewError(ctx, LoopingWorkflow, iteration + 100)
    */
    
    return nil // 这里仅作示例，实际逻辑通常结合循环
}
```

**标准写法：**

```go
func LongRunningWorkflow(ctx workflow.Context, state MyState) error {
    // 建议：每处理 N 个批次，就重启一次
    const BatchSize = 1000

    for i := 0; i < BatchSize; i++ {
        // 执行业务逻辑 (Activity, ChildWorkflow 等)
        // ...
        
        state.Counter++
    }

    // 循环结束，历史记录可能已经积累了几千条 Event
    // 此时发起 ContinueAsNew，把最新的 state 传给下一代
    return workflow.NewContinueAsNewError(ctx, LongRunningWorkflow, state)
}
```

---

## 5.22 Side Effects

`workflow.SideEffect` 是 Temporal 中用于在 Workflow 内部执行**非确定性（Non-deterministic）**代码的一种机制。

### 5.22.1 背景

Temporal 的 Workflow 必须是确定性（Deterministic）的。这意味着：无论代码重放（Replay）多少次，只要输入和历史事件相同，代码的执行路径和结果必须完全一致。如果你直接在 Workflow 代码里写：

```go
// 错误示范！
randomNum := rand.Intn(100) // 第一次运行可能是 50，重放时可能是 99
uuid := uuid.New()          // 每次运行都会生成不同的 ID
```

当 Workflow 因为 Worker 重启而重放历史时，Temporal 会发现："上次执行这里生成了 ID A，怎么这次重放生成了 ID B？"从而导致 Non-Deterministic Error，Workflow 会崩溃。

### 5.22.2 工作原理

- **首次执行时**：Worker 执行 SideEffect 中的函数（例如生成一个随机数）。
- **记录结果**：Temporal 将这个函数的返回值记录到 Workflow 的执行历史（History）中（事件类型通常是 MarkerRecorded）。
- **重放（Replay）时**：Worker 不会再次执行这个函数，而是直接从历史记录中读取上次保存的返回值。

### 5.22.3 使用示例

```go
import (
    "github.com/google/uuid"
    "go.temporal.io/sdk/workflow"
)

func MyWorkflow(ctx workflow.Context) error {
    // 使用 SideEffect 安全地生成 UUID
    var traceID string
    encodedValue := workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
        // 这里是"不安全"的代码，只会在第一次执行时运行
        return uuid.New().String()
    })
    
    // 从 encodedValue 中获取结果
    if err := encodedValue.Get(&traceID); err != nil {
        return err
    }

    // 此时 traceID 在所有重放中都是固定的
    workflow.GetLogger(ctx).Info("Generated Trace ID", "id", traceID)
    return nil
}
```

### 5.22.4 与 Activity 对比

| 特性 | SideEffect | Activity |
|------|------------|----------|
| **适用场景** | 轻量级、快速的非确定性逻辑。 | 重量级、耗时、易失败的逻辑。 |
| **典型例子** | 生成 UUID、获取随机数、读取某些配置标志。 | 读写数据库、调用第三方 API、文件处理。 |
| **开销** | 极低。不需要调度到 Task Queue，直接在当前 Worker 线程执行。 | 较高。涉及网络传输、序列化、调度等待。 |
| **重试机制** | 无重试。如果函数 panic，Workflow 任务直接失败。 | 有重试。可以配置复杂的 Retry Policy。 |
| **超时控制** | 无。必须瞬间完成。 | 有。可以配置 StartToCloseTimeout 等。 |

### 5.22.5 Mutable Side Effects

SideEffect 的值一旦记录就永久不变，而 MutableSideEffect 允许在值发生变化时更新历史记录。

#### 5.22.5.1 背景

MutableSideEffect 最典型的场景是：轮询（Polling）某个可能会变化的外部状态，但只有在状态真的变了的时候，才记录新的历史。

假设你的 Workflow 需要每隔 1 分钟检查一次配置中心（如 Consul/Etcd）里的一个开关 FeatureFlag。

- **如果用 Activity**：每分钟都会产生一个 ActivityTaskScheduled 和 ActivityTaskCompleted 事件。跑几天后，历史记录里全是这些废话，很快就会达到 5 万条限制。
- **如果用 SideEffect**：它只记录一次。如果开关后来变了，Workflow 重放时还是用的第一次记录的旧值，逻辑就错了。
- **用 MutableSideEffect**：它会定期检查值。如果值没变，它什么都不做（不写历史）；如果值变了，它会记录一个新的 MarkerRecorded 事件。这样既保证了数据的实时性，又极大节省了历史记录空间。

#### 5.22.5.2 工作原理

MutableSideEffect 接收一个自定义 ID（用于标识这个副作用）和一个函数。

1. **执行函数**：Worker 执行你提供的函数，获取最新的值（例如 true）。
2. **比对历史**：
   - 如果这是第一次执行，或者这个 ID 上次记录的值与现在不同（例如上次是 false，现在是 true）：Temporal 会在历史记录中写入一个新的 Marker 事件，记录新值。
   - 如果算出来的值与上次记录的值完全相同：Temporal 不会写入任何新的历史事件。它直接返回上次的值。
3. **返回值**：返回最新的值给 Workflow 代码。

#### 5.22.5.3 代码示例

```go
func MyWorkflow(ctx workflow.Context) error {
    // 定义一个唯一的 ID，用于标识这个 MutableSideEffect
    const configID = "max_retry_config"

    for {
        var maxRetry int
        
        // 调用 MutableSideEffect
        // 注意：这个函数会被频繁调用（每次循环都会调）
        // 但只有当返回值改变时，才会增加历史记录
        encodedVal := workflow.MutableSideEffect(ctx, configID, func(ctx workflow.Context) interface{} {
            // 这里是读取外部配置的逻辑
            // 比如读取本地缓存的全局变量，或者读取某个文件
            // 注意：这里不能做耗时的网络请求，必须非常快
            return GetGlobalConfig().MaxRetryCount
        })

        if err := encodedVal.Get(&maxRetry); err != nil {
            return err
        }

        workflow.GetLogger(ctx).Info("Current Max Retry", "val", maxRetry)

        // 模拟业务逻辑
        if someCondition(maxRetry) {
            break
        }

        // 每隔 10 秒检查一次
        workflow.Sleep(ctx, 10*time.Second)
    }
    return nil
}

// 模拟外部配置源
func GetGlobalConfig() Config {
    // ...
}
```

---

## 5.23 LocalActivity

与普通 Activity 区别如下：

### 1. 执行位置不同

- **Local Activity**：在工作流 Worker 本地执行，无需 Temporal Server 调度
- **普通 Activity**：由 Temporal Server 调度给专门的 Activity Worker 执行
- **Local Activity 相比于 普通 Activity 的延迟开销更低**，在 Temporal 通信次数更少

### 2. 序列化

- **Local Activity**：直接传参，不序列化
- **普通 Activity**：输入输出都需要序列化

### 3. 功能区别

- **Local Activity**：无 HeartBeat 机制
- **普通 Activity**：有 HeartBeat 机制

### 4. DB 写入次数

- **Local Activity**：3 次，次数更少
- **普通 Activity**：9 次，次数更多

### 5. 执行流程区别

| 步骤 | 普通 Activity | Local Activity |
|------|---------------|----------------|
| 1 | 工作流启动，将一个工作流任务添加到工作流任务队列中 | 工作流启动，将一个工作流任务添加到工作流任务队列中 |
| 2 | 监听工作流任务队列的工作流 Worker 接收到该工作流任务 | 监听工作流任务队列的工作流 Worker 接收到该工作流任务 |
| 3 | 工作流任务以 ScheduleActivityTask 命令完成 | 工作流将一个本地活动任务调度到进行中的本地活动任务队列 |
| 4 | 一个 Activity Task 被添加到活动任务队列中 | 本地活动由监听进行中本地活动任务队列的本地活动 Worker 执行 |
| 5 | Activity Worker 接收到该活动任务 | 本地活动任务完成后，结果返回给工作流 |
| 6 | Activity Worker 完成该活动任务 | 工作流任务通过 RecordMarker 和 CompleteWorkflowExecution 命令完成。其中，标记命令（marker command）包含了本地活动的执行结果 |
| 7 | 工作流任务被重新添加到工作流任务队列中 | - |
| 8 | 工作流 Worker 接收到携带活动执行结果的工作流任务 | - |
| 9 | 工作流任务以 CompleteWorkflowExecution 命令完成 | - |

Local Activity 的 3-5 步均在工作流线程的内存中执行，无需写入 DB。且对于普通 Activity 和 Local Activity 的 DB 表的大小也不同，后者远小于前者，所以在触发重试的时候后者对于 DB 的压力会更小。

**Local Activity 缺点：**

1. **仅适用于执行时长不超过工作流任务超时时间的短耗时活动**。这意味着本地活动不支持心跳机制。
2. **不适合长间隔重试场景**。当重试间隔超过工作流任务超时时间时，系统会通过定时器调度下一次重试，每次重试都会在工作流历史中新增多个事件。而普通活动几乎支持无限次数的重试。
3. **本地活动遵循至少执行一次（at least once）的语义**。一旦工作流任务执行失败，本地活动会被重新执行，这包括整个本地活动序列的全量重跑。
4. **本地活动会延长工作流任务的执行时长**。在本地活动运行期间，工作流无法响应信号，从而增加了信号处理的延迟。

---

# 6. API 使用

## 6.1 Workflow.Sleep(ctx, duration)

| 特性 | time.Sleep() | workflow.Sleep() |
|------|--------------|------------------|
| **CPU 占用** | 极低 (调度器开销) | 0 |
| **内存占用** | 占用 (Goroutine 栈 + 上下文) | 可为 0 (支持从内存卸载，仅存数据库) |
| **进程重启** | 状态丢失 (睡到一半没了) | 状态保留 (重启后接着睡/接着醒) |
| **并发上限** | 受限于内存 (单机几万到几十万) | 无限 (受限于数据库磁盘，单机可撑数亿) |

## 6.2 Workflow.Now(ctx)

原生 `time.Now()` 返回当前系统时间，这在 Workflow 重放（Replay）时会变化，导致非确定性错误。`workflow.Now()` 返回的是基于 Workflow 历史记录的确定性时间。

## 6.3 workflow.NewTimer(ctx, duration)

创建一个 Future，在指定时间后就绪。函数立即返回，但返回的 Future 会在指定的持续时间 duration 后变为就绪状态。

```go
timerFuture := workflow.NewTimer(ctx, processingTimeThreshold)
selector.AddFuture(timerFuture, func(f workflow.Future) {
    // 定时器触发后的处理逻辑
})
```

## 6.4 workflow.Go(ctx, func(ctx) { ... })

原生 goroutine 的调度顺序是随机的。`workflow.Go` 启动的协程由 Temporal 的确定性调度器管理，确保在重放时执行顺序一致。Workflow 内的并发本质是由 Temporal 的调度器与事件历史驱动的"协作式并发"，而不是任意 goroutine。这种设计确保 Workflow 代码在重放（Replay）时产生完全相同的执行路径。

## 6.5 workflow.Channel

使用 `workflow.NewChannel(ctx)` 或 `workflow.NewBufferedChannel(ctx, size)` 创建。它支持在 Temporal 协程间进行确定性的数据传递。用 `workflow.Channel` 可以确保工作流在重放（replay）时保持确定性，这是 Temporal 工作流执行的核心要求。

## 6.6 workflow.Selector

原生 select 的随机性会破坏确定性。`workflow.Selector` 允许你确定性地等待 Future、Channel 或 Timer 的就绪事件。

```go
// 创建 Selector
selector := workflow.NewSelector(ctx)

// 添加 Future 等待
work := workflow.ExecuteActivity(ctx, ExampleActivity)
selector.AddFuture(work, func(f workflow.Future) {
    // Activity 完成后的处理逻辑
})

// 添加 Channel 接收
channel := workflow.GetSignalChannel(ctx, channelName)
selector.AddReceive(channel, func(c workflow.ReceiveChannel, more bool) {
    c.Receive(ctx, &signalVal)
    // 处理接收到的信号
})

// 等待事件就绪
selector.Select(ctx)
```

## 6.7 workflow.WaitGroup

类似于 `sync.WaitGroup`，但用于等待 `workflow.Go` 启动的协程。在 Temporal 工作流中，虽然多个协程可以并行启动，但同一时间只有一个协程处于活动状态，其他协程会被阻塞。这意味着不需要担心传统并发编程中的竞态条件问题，也不需要使用互斥锁或原子操作。

```go
func workflow_main(ctx workflow.Context) {
    var wg workflow.WaitGroup
    resultChan := workflow.NewChannel(ctx)
    
    // 设置等待的协程数量
    wg.Add(5)
    
    // 启动多个协程
    for i := 0; i < 5; i++ {
        workflow.Go(ctx, func(ctx workflow.Context) {
            defer wg.Done()
            // 执行一些工作
            result := i * 2
            resultChan.Send(ctx, result)
        })
    }
    
    // 等待所有协程完成
    wg.Wait()
    resultChan.Close()
}
```

## 6.8 workflow.Context

Workflow 函数的第一个参数必须是 `workflow.Context`。它携带了 Workflow 的状态、配置和取消信号，不能使用标准的 `context.Background()` 或 `context.TODO()`。`workflow.Context` 提供了在多个 goroutine 之间传递"上下文"的机制，主要用于控制并发任务的生命周期。

## 6.9 workflow.SideEffect(...)

如果需要在 Workflow 中生成随机数或 UUID，必须使用 SideEffect。它会执行一次并将结果记录到历史日志中，重放时直接返回历史记录中的值，而不会重新生成。

## 6.10 workflow.GetLogger(ctx)

使用 `workflow.GetLogger` 获取的 Logger 具备"重放感知"能力。在 Workflow 重放期间，它会自动抑制重复的日志输出，避免日志被历史数据淹没。

## 6.11 workflow.NewFuture(ctx)

```go
future, settable := workflow.NewFuture(ctx)

workflow.Go(ctx, func(ctx workflow.Context) {
    err := exe.execute(ctx, bindings)
    settable.Set(nil, err)
})

return future
```

// 当这个协程里的 exe.execute 跑完时（不管成功还是失败），它调用 settable.Set(nil, err)。
// 动作：这会触发 Temporal 的事件通知机制。
// 结果：任何正在 future.Get() 上等待的代码，会立刻解除阻塞，并拿到这里的 nil 和 err。
