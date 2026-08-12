# AGENTS.md

## 项目概况

这是一个跨 Windows/Linux 的 Go SSH/SCP wrapper。同一份源码按可执行文件名
生成 `ssh-wrapper` 和 `scp-wrapper`,每个可执行文件同时承担两个角色：

- wrapper 模式：解析目标 Host，读取 TOML，设置 askpass 环境并运行系统 OpenSSH。
- askpass 模式：由 OpenSSH 回调，根据环境中的 Host 别名输出对应密码。

程序不实现 SSH 协议，不使用 PTY，也不监听密码提示。核心入口位于 `main.go`，
测试位于 `main_test.go`。

## 常用命令

构建当前平台产物：

```powershell
nu build.nu
```

或：

```sh
bash build.sh
```

两个脚本会构建 SSH/SCP 两个产物，并保留已有的
`build/ssh-wrapper-config.toml`,只在该文件不存在时复制项目示例配置.修改构建
流程时必须保持这一行为，避免覆盖用户的真实服务器密码.

格式化和测试：

```powershell
gofmt -w main.go main_test.go
go test ./...
go vet ./...
```

Windows 构建：

```powershell
go build -o build\ssh-wrapper.exe .
go build -o build\scp-wrapper.exe .
```

Windows 上检查 Linux 构建：

```powershell
$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -o build\ssh-wrapper-linux-amd64 .
go build -o build\scp-wrapper-linux-amd64 .
```

不要使用 `GOOS=linux go test` 直接执行交叉编译后的测试二进制。

## 代码结构与约束

- `run` 负责区分 wrapper、askpass 和管理命令。
- `invocationClient` 根据可执行文件名选择真实 `ssh` 或 `scp`；构建脚本必须从
  同一 package 生成两个正确命名的产物.
- `sshDestinationIndex` 负责寻找 OpenSSH 目标参数。修改参数解析时必须覆盖
  `-V`、`-G`、带参数短选项、组合短选项、`--` 和远程命令。
- `configuredSSHArgs` 必须把 TOML 默认值插在目标 Host 之前，并保留用户显式
  SSH 选项的更高优先级。目标 Host 后面的参数属于远程命令，不能重排。
- `scpRemoteAliases` 必须识别 `host:path`、`user@host:path`、`scp://` URI 和
  Windows 盘符路径；不同远端 Host 的自动密码复制必须明确拒绝.
- `executeClient` 必须透传 stdin/stdout/stderr 和 OpenSSH 退出码。
- `SSH_ASKPASS_REQUIRE=force`、`SSH_ASKPASS`、`DISPLAY` 以及内部模式/别名环境
  变量是 askpass 调用链的一部分，修改时必须同时验证 wrapper 与 askpass 分支。
- 未命中 TOML 的 Host 必须原样透传，不得因为配置缺项破坏已有
  `~/.ssh/config` 主机。
- `-V`、无目标参数的查询命令在 TOML 不存在时也必须正常工作。

## 配置规则

- 正式配置名为 `ssh-wrapper-config.toml`,默认和可执行文件同目录.
- `SSH_WRAPPER_CONFIG` 只用于覆盖配置路径，适合开发和测试。
- 项目中的 `ssh-wrapper-config.toml` 是可提交示例，只能使用假密码 `change-me`；
  不要在仓库文件、测试或文档中写真实密码。
- 本项目源码公开，严禁向代码仓库（包括源码、测试、文档、示例配置、日志和 Git
  历史）提交真实服务器 IP、Host、用户名、密码或其他内部连接信息。示例地址只能
  使用 RFC 5737 保留网段（如 `192.0.2.0/24`）、`example.*` 域名和明显的占位密码
  （如 `change-me`）。提交前必须检查暂存内容；一旦误提交，删除当前文件并不足以
  清除 Git 历史，必须立即按凭据泄露处理。
- 部署时应在 `build/` 或其他分发目录修改配置；`build/` 已被 `.gitignore` 忽略.
- Host 别名必须是 OpenSSH 可直接使用的字面名称，不支持 `*`、`?` 等模式。
- `Password` 不能通过 `options` 进入 OpenSSH config；生成配置必须始终不含密码。
- `host` 和 `hostname` 是兼容字段，新文档和示例优先使用 `host`。
- TOML 同时支持 `[servers]` 下的内联服务器表和 `[servers."alias"]` 展开子表；
  常规示例优先用前者，需要 `options` 或详细注释时可用后者.

## VSCode 与 OpenSSH config

VSCode Remote-SSH 从 `~/.ssh/config` 枚举 Host。`--wrapper-install` 会：

1. 在 TOML 旁写入 `ssh-wrapper.hosts.conf`。
2. 在用户 SSH config 顶部加入一条 `Include`。

安装逻辑必须满足：

- 不覆盖或删除用户原有 SSH config 内容。
- 重复执行是幂等的，不重复写入 `Include`。
- 生成文件不包含密码。
- Host 按别名排序，以保持输出稳定。

修改安装逻辑时只在临时 HOME/USERPROFILE 下测试，开发验证不得直接改
`C:\Users\tom\.ssh\config`。

## 测试要求

行为修改至少覆盖对应单元测试。高风险路径包括：

- TOML 缺失、语法错误、字段冲突和非法端口。
- 不同 Host 对应不同密码。
- 主机密钥确认提示不得被误当成密码提示。
- SSH 参数中的目标识别和命令行优先级。
- SCP 上传、下载、URI、Windows 路径、选项参数和多远端拒绝.
- 环境变量替换在 Windows 上大小写不敏感。
- OpenSSH config 生成不泄露密码。
- `--wrapper-install` 保留原配置且重复执行结果一致。

完成修改后至少运行 `go test ./...`、`go vet ./...` 和 Windows/Linux 两个平台的
构建检查。

## 安全边界

明文密码是本项目明确接受的业务要求，不要擅自改为交互输入、系统凭据库或加密
格式，从而破坏无人值守登录。可以增加可选的安全后端，但必须保留现有 TOML 行为
和兼容性。

日志、错误、测试失败信息、生成的 OpenSSH config 和管理命令输出都不得打印
密码。不要运行会真实连接公司服务器或修改用户 SSH 配置的测试，除非用户明确
要求。
