# ssh-wrapper

<p align="center">
  <img src="docs/assets/you-shall-pass.png" alt="You shall pass" width="512">
</p>

`ssh-wrapper` / `scp-wrapper` 是同时充当 OpenSSH wrapper 和 `SSH_ASKPASS`
provider 的 Go 程序,用于按 SSH Host 别名自动选择密码.它支持 Windows 和
Linux；`ssh-wrapper` 可作为 VSCode Remote-SSH 的 `remote.SSH.path`,两个程序
也可分别直接代替 `ssh` 和 `scp` 使用.

程序不会实现 SSH 协议,而是把参数透传给系统 OpenSSH.只有
`ssh-wrapper-config.toml` 中配置的别名会启用自动密码；其他 Host（例如使用
密钥登录的 GitHub）仍由原有 `~/.ssh/config` 和 OpenSSH 正常处理.

## 工作原理

1. wrapper 从 SSH 或 SCP 参数中识别目标 Host 别名.
2. 如果别名存在于 TOML,wrapper 注入对应的 `HostName`、`Port`、`User` 和
   `SSH_ASKPASS` 环境变量,然后启动真正的 OpenSSH.
3. OpenSSH 请求密码时,再次调用同一个可执行文件.
4. 程序在 askpass 模式下根据别名输出对应密码.
5. 如果别名未配置,所有参数和标准输入输出直接交给 OpenSSH.

VSCode 常用的 `ssh -V`、`ssh -G`、端口转发、远程命令，以及 SCP 上传和下载
均保持兼容.

参数兼容性、安全边界和本轮修复记录见
[SSH/SCP wrapper 实现审阅记录](docs/implementation-review.md).

## 构建

要求 Go 1.23 或更高版本.

使用 Nushell（Windows/Linux 均可）：

```text
nu build.nu
```

使用 Bash（Linux、WSL 或 Git Bash）：

```sh
bash build.sh
```

脚本会执行 `go fmt ./...`,以 `CGO_ENABLED=0` 和 `-ldflags="-s -w"` 构建
当前 Go 工具链对应平台的 `ssh-wrapper` 和 `scp-wrapper` 到 `build/`.WSL 中
若只安装了 Windows `go.exe`,`build.sh` 会生成两个 `.exe` 文件.如果
`build/ssh-wrapper-config.toml` 不存在,脚本会从项目示例创建一份；已有配置
不会被覆盖.

也可以手动构建.Windows：

```powershell
New-Item -ItemType Directory -Force build
go build -o build\ssh-wrapper.exe .
go build -o build\scp-wrapper.exe .
Copy-Item ssh-wrapper-config.toml build\ssh-wrapper-config.toml
```

Linux：

```sh
mkdir -p build
go build -o build/ssh-wrapper .
go build -o build/scp-wrapper .
cp ssh-wrapper-config.toml build/ssh-wrapper-config.toml
chmod 600 build/ssh-wrapper-config.toml
```

默认情况下,`ssh-wrapper-config.toml` 必须和可执行文件放在同一目录.两个
wrapper 应放在同一目录并共享该配置.开发和测试时可通过
`SSH_WRAPPER_CONFIG` 指定其他配置路径.

## 配置

项目自带脱敏示例 [ssh-wrapper-config.toml](ssh-wrapper-config.toml)，其中只使用
RFC 5737 保留地址和占位密码 `change-me`：

```toml
[wrapper]
force_password_auth = true
# real_ssh = "C:/Windows/System32/OpenSSH/ssh.exe"
# real_scp = "C:/Windows/System32/OpenSSH/scp.exe"

[servers]
"example-a" = { host = "192.0.2.10", port = 13001, user = "app", password = "change-me" }
"example-b" = { host = "192.0.2.20", port = 11061, user = "app", password = "change-me" }
```

父表加内联表适合结构一致的服务器列表.如果某台服务器需要较多注释或额外
OpenSSH 选项，也可以使用等价的展开格式：

```toml
[servers."example-b"]
host = "192.0.2.20"
port = 11061
user = "app"
password = "change-me"

[servers."example-b".options]
StrictHostKeyChecking = "accept-new"
```

配置字段：

| 字段                          | 必需 | 说明                                           |
| ----------------------------- | ---- | ---------------------------------------------- |
| `servers."alias"`             | 是   | OpenSSH `Host` 别名,不支持通配符               |
| `host` / `hostname`           | 是   | 服务器地址,二选一；两者同时出现时必须相同      |
| `port`                        | 是   | SSH 端口,范围 `1..65535`                       |
| `user`                        | 是   | SSH 用户名                                     |
| `password`                    | 是   | 明文密码,允许空字符串                          |
| `options`                     | 否   | 额外 OpenSSH 配置,按 `-o Name=Value` 传入      |
| `wrapper.real_ssh`            | 否   | 真正的 OpenSSH 路径；通常无需设置              |
| `wrapper.real_scp`            | 否   | 真正的 OpenSSH SCP 路径；通常无需设置          |
| `wrapper.force_password_auth` | 否   | 默认 `true`,强制密码/keyboard-interactive 认证 |

`options` 中不能填写 `Password`、`HostName`、`Port` 或 `User`,这些值必须使用
结构化字段配置.显式传给 wrapper 的 SSH 命令行选项优先于 TOML 默认值.

密码按需求以明文保存。由于本项目源码公开，严禁向代码仓库提交真实服务器 IP、
Host、用户名、密码或其他内部连接信息；此要求同时适用于源码、测试、文档、示例
配置、日志和 Git 历史。仓库示例只能使用 RFC 5737 保留地址、`example.*` 域名及
`change-me` 等明显占位值。实际配置应只在已忽略的 `build/` 或仓库外维护。Linux
上可将其权限设为 `0600`，Windows 上应限制该文件的 ACL。

## VSCode Host 列表

VSCode 从 `~/.ssh/config` 获取 Host 列表,不会读取 TOML.
因此 TOML 中新增别名后,需要运行：

```powershell
.\build\ssh-wrapper.exe --wrapper-install
```

Linux：

```sh
./build/ssh-wrapper --wrapper-install
```

该命令执行两项操作：

- 在 TOML 旁生成不含密码的 `ssh-wrapper.hosts.conf`.
- 在 `~/.ssh/config` 顶部添加一条指向该文件的 `Include`.

已有 `.ssh/config` 内容会保留；重复执行不会重复添加 `Include`.
修改 TOML 后再次运行命令即可刷新 Host 列表.

TOML 的 Host 集合不需要覆盖 `.ssh/config` 的全部 Host.
未出现在 TOML 中的Host 仍会透传给 OpenSSH,只是不会自动输入密码.

>当然, 也可以手动维护 `~/.ssh/config`, 只要两份文件内 server 命名一致即可

## 配置 VSCode

Windows `settings.json`：

```json
{
  "remote.SSH.path": "C:\\tools\\ssh-wrapper\\ssh-wrapper.exe",
  "remote.SSH.useLocalServer": false,
  "remote.SSH.showLoginTerminal": false
}
```

Linux 将 `remote.SSH.path` 改为 Linux 可执行文件的绝对路径.两个平台都不需要
单独的 askpass 脚本.

## 使用

wrapper 的普通参数与 OpenSSH 相同：

```bash
ssh-wrapper 12.11061
ssh-wrapper 12.11061 echo hello
ssh-wrapper -L 6801:localhost:28001 12.11061
ssh-wrapper -V
ssh-wrapper -G 12.11061
```

SCP 上传和下载：

```bash
scp-wrapper local.zip 12.11061:/tmp/
scp-wrapper 12.11061:/tmp/result.zip .
scp-wrapper -r local-dir 12.11061:/tmp/
scp-wrapper scp://app@12.11061:11061/tmp/result.zip .
```

一条 SCP 命令可以包含多个本地文件，也可包含同一远端 Host 的多个路径.因为
OpenSSH SCP 的连接选项作用于整条命令，自动密码模式不支持在一条命令中连接
两个不同的远端 Host；程序会明确报错而不是使用错误密码.

管理命令：

```bash
ssh-wrapper --wrapper-list          # 列出 TOML 中的服务器
ssh-wrapper --wrapper-print-config  # 输出将生成的 OpenSSH Host 配置
ssh-wrapper --wrapper-install       # 生成 Host 配置并安装 Include
ssh-wrapper --wrapper-version
ssh-wrapper --wrapper-help
```

## 开发与验证

```powershell
go test ./...
go vet ./...
go build -o build\ssh-wrapper.exe .
go build -o build\scp-wrapper.exe .
```

在 Windows 上检查 Linux 构建：

```powershell
$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -o build\ssh-wrapper-linux-amd64 .
go build -o build\scp-wrapper-linux-amd64 .
```

无需连接服务器即可检查 OpenSSH 配置解析：

```powershell
$env:SSH_WRAPPER_CONFIG = (Resolve-Path .\ssh-wrapper-config.toml).Path
.\build\ssh-wrapper.exe -G 12.11061
```

## 常见问题

### VSCode 里看不到 TOML 新增的 Host

运行 `--wrapper-install`,然后重新打开 Remote-SSH Host 列表.VSCode 只枚举
OpenSSH config,不直接解析 TOML.

### 仍然弹出密码输入框

确认 VSCode 的 `remote.SSH.path` 指向 wrapper,并将
`remote.SSH.showLoginTerminal` 设为 `false`.再用 `--wrapper-list` 检查别名是否
确实存在于当前 TOML.

### 找不到真正的 ssh 或 scp

Windows 默认在 `%WINDIR%\System32\OpenSSH` 下查找 `ssh.exe` / `scp.exe`；
Linux 默认查找 `/usr/bin`、`/usr/local/bin` 和 `PATH`.也可以显式配置：

```toml
[wrapper]
real_ssh = "C:/Windows/System32/OpenSSH/ssh.exe"
real_scp = "C:/Windows/System32/OpenSSH/scp.exe"
```

### 首次连接被拒绝

askpass 不会自动接受未知主机密钥.建议先人工核对主机指纹,或在对应服务器的
`options` 中按公司安全策略设置 `StrictHostKeyChecking`.

## 安全说明

本项目面向明确接受明文密码的内部使用场景.密码不会写入生成的
`ssh-wrapper.hosts.conf`,也不会作为 OpenSSH 命令行参数传递,但仍以明文存在
于 TOML 和 askpass 子进程输出中.不要将配置文件用于不受信任的机器或多人共享
目录。绝对不要把包含真实服务器地址或密码的部署配置提交到本代码仓库。
