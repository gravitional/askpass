# SSH/SCP wrapper 实现审阅记录

本文记录 2026-08-12 为 `scp-wrapper`、TOML 配置改名及紧凑服务器表实现所做的
代码审阅。审阅范围包括 OpenSSH 参数透传、SCP operand 解析、配置优先级、
askpass 环境注入、Windows/Linux 差异和构建产物。

对应版本：`ssh-wrapper 0.2.0`。

## 审阅结论

实现采用一个 Go package 构建两个可执行文件：

- `ssh-wrapper` 调用真实 OpenSSH `ssh`。
- `scp-wrapper` 调用真实 OpenSSH `scp`。

程序根据可执行文件名选择客户端，共用 `ssh-wrapper-config.toml` 和 askpass
逻辑。审阅中发现的问题均已修复并加入回归测试。没有执行真实服务器连接或文件
复制，也没有修改用户的 `~/.ssh/config`。

## 1. 组合短选项误判目标

触发示例：

```text
ssh-wrapper -vp 2200 12.11061
scp-wrapper -vP 2200 local.zip 12.11061:/tmp/
```

原解析器只检查短选项簇的第一个字符。`-vp` 中的 `p` 和 `-vP` 中的 `P` 都需要
消费下一个参数，但端口值会被误认成目标 Host 或 SCP operand。

修复：使用表驱动的 `shortOptionConsumesNext` 扫描整个短选项簇，同时支持附加值
形式，例如 `-P2200` 和 `-Fcustom.conf`。

验证覆盖：SSH/SCP 分离参数、组合参数和附加值参数。

## 2. `--` 后注入 OpenSSH 选项

触发示例：

```text
ssh-wrapper -- 12.11061 echo hello
scp-wrapper -- local.zip 12.11061:/tmp/
```

`--` 表示选项结束。如果将 TOML 生成的 `-o HostName=...` 等参数插入 `--` 后，
OpenSSH 会把它们当作目标或文件名。

修复：`optionInsertionIndex` 检测目标前的 `--`，将所有注入选项放在 `--` 之前。

验证覆盖：SSH 和 SCP 的 `--` 调用形式。

## 3. 本地含冒号路径被识别为远端

可能误判的路径：

```text
C:\tmp\result.zip
E:relative.txt
./archive:old.zip
/tmp/result:old.zip
```

SCP 使用 `host:path` 表示远端路径，但 Windows 盘符和 Unix 文件名也可能包含
冒号。误判后 wrapper 会尝试按 Host 查找密码和注入服务器配置。

修复：

- Windows 下将单字母盘符前缀识别为本地路径。
- 冒号前已出现 `/` 或 `\` 时识别为本地路径。
- 盘符规则只在 Windows 生效；Linux 上 `C:path` 仍遵循 SCP 远端语义。

验证覆盖：Windows 绝对/drive-relative 路径、Unix 含冒号路径和普通远端路径。

## 4. IPv6 和 SCP URI 解析不完整

需要支持的形式：

```text
[2001:db8::1]:/tmp/a
app@[2001:db8::1]:/tmp/a
scp://app@12.11061:11061/tmp/a
```

简单地查找第一个冒号无法解析 IPv6；带用户名的 bracketed IPv6 还需要先跳过
`user@`。

修复：`scpRemoteSpec` 分别解析 `scp://` URI、bracketed IPv6 和传统
`user@host:path`，统一返回 alias 以及 operand 是否显式指定 user/port。

验证覆盖：IPv4/别名、IPv6、带用户名 IPv6 和 SCP URI。

## 5. TOML 覆盖 SCP operand 中的显式 user/port

触发示例：

```text
scp-wrapper other@12.11061:/tmp/a .
scp-wrapper scp://other@12.11061:2200/tmp/a .
```

按照 OpenSSH 命令行语义，operand 中显式提供的用户名和 URI 端口应优先于 TOML
默认值。原实现无条件注入 `User=app` 和配置端口，可能连接错误账户或端口。

修复：`scpOperandOverrides` 检查远端 operand；显式 user/port 存在时不注入对应
TOML 默认值，仍保留 HostName 和其他认证选项。

验证覆盖：传统 `user@host:path` 和含 user/port 的 `scp://` URI。

## 6. 多远端 SCP 可能套用错误配置

危险示例：

```text
scp-wrapper 12.11061:/a 5.13001:/b
scp-wrapper 12.11061:/a unconfigured:/b
```

OpenSSH SCP 的 `-o HostName/Port/User` 对整条命令生效，不能为两个不同远端分别
注入两套配置或密码。即使只有一端存在于 TOML，继续执行也可能将另一端改写为
错误服务器。

修复：只要命令包含多个不同远端 alias，且其中至少一个启用了自动密码，就明确
返回错误。同一远端的多个路径仍允许使用。

当前设计限制：自动密码模式不支持远端到不同远端复制。应拆成两条命令并通过
本地临时文件中转。

## 7. `real_scp` 可能被系统查找提前阻断

用户可以在配置中指定：

```toml
[wrapper]
real_scp = "C:/custom/OpenSSH/scp.exe"
```

原执行顺序先查找系统默认 SCP，再读取 TOML。如果默认客户端不存在，即使配置了
有效的 `real_scp` 也会提前失败。

修复：存在远端目标时先加载配置，再按 `real_ssh` / `real_scp` 定位客户端；
无远端的版本、帮助或纯本地调用仍快速透传。

验证覆盖：端到端假客户端测试确认读取 `real_scp`、参数注入和 askpass 环境设置。

## 8. 显式 SSH/SCP 选项优先级

TOML 中的 HostName、Port、User 是默认值，用户在命令行显式传入的 `-p/-P`、
`-l` 或 `-o` 应优先。

修复：注入参数放在用户选项之后、目标或第一个 operand 之前。OpenSSH 对同类选项
采用第一个获得的值，因此保留用户选项优先级。目标之后的远端命令和 SCP 文件列表
不重排。

验证覆盖：SSH `-p/-l` 和 SCP `-P` 参数顺序。

## 9. 不同平台路径语义

Windows 和 Linux 对 `C:path` 的解释不同：

- Windows：drive-relative 本地路径。
- Linux：可表示远端 Host `C` 的路径。

修复：盘符判断受 `runtime.GOOS == "windows"` 限制，避免 Windows 兼容逻辑改变
Linux 原生 SCP 语义。

验证覆盖：Windows 原生测试以及 Linux 测试二进制编译检查。

## 10. 配置文件名和服务器表格式

默认配置名由容易混淆的 `ssh-wrapper.toml` 改为：

```text
ssh-wrapper-config.toml
```

默认路径始终是可执行文件同目录，也可通过 `SSH_WRAPPER_CONFIG` 覆盖。SSH/SCP
两个 wrapper 共用该文件。

服务器列表支持两种等价 TOML 格式。常规列表使用紧凑父表：

```toml
[servers]
"12.11061" = { host = "10.0.255.12", port = 11061, user = "app", password = "change-me" }
```

需要详细注释或 `options` 时使用展开子表：

```toml
[servers."12.11061"]
host = "10.0.255.12"
port = 11061
user = "app"
password = "change-me"
```

项目示例包含 14 台脱敏服务器，密码全部为 `change-me`。对应
`trps2/yangmy/registry.toml` 的服务器段也已确认是紧凑格式，并可解析出 14 台
服务器。

## Cobra 评估

审阅中考虑过使用 `github.com/spf13/cobra`。最终没有引入，原因是 wrapper 与
普通 CLI 的参数所有权不同：

- wrapper 必须接受当前及未来 OpenSSH 的未知参数并原样透传。
- Cobra 的主要价值是定义并校验本程序拥有的 flags；这里会增加未知 flag、短选项
  重组和透传兼容风险。
- 最复杂的 SCP 本地/远端路径区分仍需自定义解析，Cobra 无法替代。
- wrapper 自有管理命令只有少量固定无参数选项。

因此目前保留小型表驱动 scanner，并用边界测试锁定行为。如果将来管理命令扩展为
独立的多层 CLI，可只对管理命令引入 Cobra，不让它解析 OpenSSH 透传参数。

## 验证结果

审阅完成后执行：

```text
go test ./...
go vet ./...
nu build.nu
bash build.sh
Windows ssh-wrapper/scp-wrapper build
Linux ssh-wrapper/scp-wrapper build
Linux test binary compile
```

此外使用测试进程作为假的 SCP 客户端进行端到端验证，确认：

- 实际选择 `real_scp`。
- 注入 HostName/Port/User 和认证参数。
- 设置 `SSH_ASKPASS`、askpass 模式和 Host alias 环境变量。
- 参数和环境验证不需要连接真实服务器。
