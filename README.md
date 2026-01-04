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

* [ ] **Netpoll** (网络库)
  
    * [x] **LinkBuffer** 
    * [ ] **Poller** 
    * [ ] **Connection** (如何将 Buffer 和 FD 结合)
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



## 🤝 贡献

本项目主要为个人学习记录。但也非常欢迎任何形式的贡献和交流：

- 如果你发现笔记中有任何错误，欢迎提交 **Issue** 指出。
- 如果你有更好的见解或分析，欢迎提交 **Pull Request**。