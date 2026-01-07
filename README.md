# CloudWeGo 源码分析 

本项目是一个个人的学习和分析仓库，专注于深入研究 [CloudWeGo](https://www.cloudwego.io/) 生态中的各个项目（如 Kitex, Hertz, Netpoll 等）的实现细节和设计思想。

## 🚀 项目目的

CloudWeGo 作为字节跳动开源的高性能、可扩展的微服务框架，其内部包含了大量关于 Go 语言高性能实践、网络编程、并发模型和工程化的优秀设计。

本项目的主要目的：

1.  **深入学习：** 通过阅读和分析生产级别的源码，提升对 Go 语言底层、网络编程和微服务架构的理解。
2.  **解析核心组件：** 重点分析 CloudWeGo 的核心项目，包括：
    * **[Kitex](https://github.com/cloudwego/kitex):** 高性能、强可扩展的 Go RPC 框架。
    * **[Hertz](https://github.com/cloudwego/hertz):** 高性能、高易用性的 Go HTTP 框架。
    * **[Netpoll](https://github.com/cloudwego/netpoll):** 基于 Epoll 实现的高性能 Go 网络库。
3.  **记录与沉淀：** 以笔记、图表、注释和可运行的示例代码（Example）的形式，记录分析过程和学习心得。

## 📁 仓库结构

本仓库计划按 CloudWeGo 的不同项目进行分模块组织，每个模块下包含分析笔记和相关的示例代码。



## 📊 分析进度

这是一个动态更新的列表，用于跟踪各个模块的分析进度。

* [x] **Netpoll** (网络库)

    * [x] **LinkBuffer**
        * [x] 核心架构与零拷贝设计
        * [x] linkBufferNode 数据结构
        * [x] Reader 接口（Next、Peek、Skip、Release、Slice 等）
        * [x] Writer 接口（Malloc、Flush、WriteBinary、WriteDirect 等）
        * [x] Connection 接口方法
        * [x] 零拷贝与引用计数机制
    * [x] **Poller**
        * [x] 核心概念与架构（epoll/kqueue）
        * [x] Linux 平台实现（poll_default_linux）
        * [x] 初始化与核心结构（eventfd、FDOperator）
        * [x] 事件循环与控制接口
    * [x] **Connection** (Buffer 和 FD 的完美结合)
        * [x] Connection 接口定义与核心结构
        * [x] Reader/Writer 实现详解（零拷贝读写）
        * [x] 与 FDOperator 的深度集成
        * [x] 事件处理机制（OnConnect/OnRequest/OnDisconnect）
        * [x] 完整的生命周期管理
    * [ ] **EventLoop** (事件分发逻辑)
    * [ ] **Server/Listener** (整体组装)
* [ ] **Kitex** (RPC 框架)

    * [ ] 服务端启动流程
    * [ ] 客户端调用流程
    * [ ] 编解码 (Codec) 与协议 (Protocol)
    * [ ] 传输层 (Transport) 与 Netpoll 的结合
    * [ ] 服务治理 (熔断、限流、重试)
* [ ] **Hertz** (HTTP 框架)
    * [ ] 路由树 (Routing)
    * [ ] 中间件 (Middleware) 机制
    * [ ] 协议层 (HTTP/1.1, HTTP/2)
    * [ ] Netpoll / Golang 原生库的适配
    
    

## 📖 如何使用

你可以克隆本项目，并跟随每个子目录下的 `README.md` 或笔记文档开始阅读。

```bash
git clone https://github.com/Lzww0608/sourcecodeanalysis.git
cd sourcecodeanalysis
```



## 📝 未来计划

基于已完成的 Netpoll 核心组件分析，后续将按以下路径继续深入：

### Netpoll 进阶分析

1. **Connection 层** - 研究 LinkBuffer 如何与文件描述符（FD）结合
   - Connection 接口的实现机制
   - 读写操作与 Netpoll 的集成
   - 连接状态管理与生命周期

2. **EventLoop 与事件分发** - 深入理解事件驱动模型
   - EventLoop 的工作机制
   - OnRequest 回调的触发流程
   - 事件分发与任务调度

3. **Server/Listener** - 完整服务端架构
   - 服务端启动流程
   - 监听器实现
   - Gopool 协程池的使用

### Kitex 深度解析

基于 `kitex/plan.md` 中制定的 30 天学习计划，将按以下阶段推进：

**第一阶段：基础构建与网络模型理解（第 1-7 天）**
- Go 网络编程进阶（IO 模型、EventLoop、OnRequest）
- IDL 与 Thrift 协议
- Kitex Hello World 与抓包分析

**第二阶段：核心源码阅读与调试（第 8-14 天）**
- Client 端源码追踪（Call → Netpoll.Write）
- Server 端与 Handler 机制

**第三阶段：中间件与治理能力实战（第 15-21 天）**
- 自定义中间件开发（全链路灰度染色）
- 服务发现与负载均衡
- 泛化调用实验

**第四阶段：高级话题与架构思考（第 22-30 天）**
- Proxyless Mesh 与 xDS
- 性能调优与 Benchmark
- 故障模拟与最佳实践

### 已完成的 Kitex 概念文档

- ✅ **Thrift 协议详解** - IDL 语法、架构分层、代码生成
- ✅ **Kitex 概念介绍** - 架构设计、核心特点、适用场景

---

## 🤝 贡献

本项目主要为个人学习记录。但也非常欢迎任何形式的贡献和交流：

- 如果你发现笔记中有任何错误，欢迎提交 **Issue** 指出。
- 如果你有更好的见解或分析，欢迎提交 **Pull Request**。