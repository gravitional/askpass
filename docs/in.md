# in.md

要让 VSCode 自动输入密码, 最干净的办法是利用 OpenSSH 原生的 **`SSH_ASKPASS`** 机制:
当 ssh 需要密码且没有终端时, 它会调用一个外部程序, 该程序把密码打印到 stdout, ssh 直接读取.
这样 VSCode 监控不到 `"password:"` 字符串, 也就不会弹窗.

因为你已经用 Go 写了一个 sshpass, 所以再写一个小程序配合它即可.

---

## 核心原理

| 组件              | 作用                                                            |
| ----------------- | --------------------------------------------------------------- |
| `ssh-wrapper.exe` | VSCode 实际调用的"伪 ssh", 它设置环境变量后启动真正的 `ssh.exe` |
| `SSH_ASKPASS`     | 指向一个程序, ssh 在需要密码时会静默调用它                      |
| `askpass.exe`     | 被 ssh 调用时, 直接把密码输出到 stdout                          |

> **为什么不是直接让 VSCode 调用 `sshpsgo`?**
> VSCode 会频繁调用 ssh 执行 `-V`(查版本), `-G`(查配置)等命令, 如果 `sshpsgo` 不是完美透传所有参数, VSCode 会报错. 而 `SSH_ASKPASS` 是标准扩展点, 最稳定.

---

## Windows 完整配置

### 1. 写一个自包含的 Go wrapper(单文件)

这个程序同时充当 **wrapper** 和 **askpass**. 当它被 ssh 作为 askpass 调用时, 直接输出密码;当被 VSCode 调用时, 启动真正的 ssh 并注入 `SSH_ASKPASS`.

```go
// ssh-wrapper.go
package main

import (
	"fmt"
	"os"
	"os/exec"
)

const (
	yourPassword = "123456"                    // 你的密码
	realSSH      = `C:\Windows\System32\OpenSSH\ssh.exe`
)

func main() {
	// 模式 A: 被 OpenSSH 作为 SSH_ASKPASS 调用时, 直接输出密码
	if os.Getenv("_IS_ASKPASS") == "1" {
		fmt.Println(yourPassword)
		return
	}

	// 模式 B: 作为 ssh 的替代品被 VSCode 调用
	self, _ := os.Executable()

	cmd := exec.Command(realSSH, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// 注入环境变量, 强制使用 askpass
	env := os.Environ()
	env = append(env, "SSH_ASKPASS="+self)
	env = append(env, "_IS_ASKPASS=1")
	env = append(env, "SSH_ASKPASS_REQUIRE=force") // OpenSSH 8.4+, 强制使用 askpass
	env = append(env, "DISPLAY=1")                 // Windows OpenSSH 兼容性需要
	cmd.Env = env

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

编译:

```bash
go build -o ssh-wrapper.exe ssh-wrapper.go
```

把 `ssh-wrapper.exe` 放到一个固定目录, 比如 `C:\tools\ssh-wrapper.exe`.

---

### 2. 配置 VSCode

打开 VSCode 的 `settings.json`(`Ctrl+Shift+P` → `Preferences: Open User Settings JSON`):

```json
{
  "remote.SSH.path": "C:\\tools\\ssh-wrapper.exe",
  "remote.SSH.useLocalServer": false,
  "remote.SSH.showLoginTerminal": false
}
```

> - `useLocalServer: false`: 确保 VSCode 直接调用你指定的 wrapper, 不走后台代理.
> - `showLoginTerminal: false`: 防止 VSCode 打开终端让你手动输密码.

---

### 3. 配置 SSH config

`C:\Users\你的用户名\.ssh\config`:

```ssh-config
Host tps-alg-dev
    HostName 10.0.255.15
    Port 11061
    User app
    StrictHostKeyChecking accept-new
    UserKnownHostsFile C:\Users\你的用户名\.ssh\known_hosts
```

然后点击 VSCode 左下角 `><` → `Connect to Host...` → `tps-alg-dev`, 应该直接连上, **全程无密码弹窗**.

---

### 4. 验证 wrapper 是否被调用

如果第一次没成功, 在 PowerShell 里手动测试 wrapper:

```powershell
$env:SSH_ASKPASS_REQUIRE="force"
$env:DISPLAY="1"
C:\tools\ssh-wrapper.exe -V
# 应该输出 OpenSSH 版本

C:\tools\ssh-wrapper.exe tps-alg-dev echo hello
# 应该直接输出 hello, 不提示密码
```

---

## Linux / macOS / WSL 的差异

在 Linux 上, 你**不需要**写 wrapper, 直接利用 `SSH_ASKPASS` 即可:

```bash
# 1. 写一个 askpass 脚本
cat > ~/.ssh/askpass.sh << 'EOF'
#!/bin/bash
echo "123456"
EOF
chmod 700 ~/.ssh/askpass.sh

# 2. 设置环境变量(可以放在 ~/.bashrc 或 VSCode 的 terminal.integrated.env.linux 中)
export SSH_ASKPASS=~/.ssh/askpass.sh
export SSH_ASKPASS_REQUIRE=force
export DISPLAY=:0  # 任意值, 让 ssh 认为有图形环境

# 3. VSCode 的 remote.SSH.path 保持默认 ssh 即可
#    因为环境变量会继承给 VSCode 启动的 ssh 进程
```

或者更常见的做法: Linux 上直接装 `sshpass`, 然后在 VSCode 的 `settings.json` 里:

```json
"remote.SSH.path": "/usr/bin/sshpass"
```

但 `sshpass` 不能直接被 VSCode 当作 ssh 用(参数不兼容), 所以 Linux 上通常也是写一个简单的 wrapper shell 脚本:

```bash
#!/bin/bash
# ~/.ssh/ssh-askpass-wrapper
export SSH_ASKPASS=/home/用户名/.ssh/askpass.sh
export SSH_ASKPASS_REQUIRE=force
export DISPLAY=:0
exec /usr/bin/ssh "$@"
```

然后 `chmod +x`, VSCode 配置 `"remote.SSH.path": "/home/用户名/.ssh/ssh-askpass-wrapper"`.

---

## 常见问题排查

| 现象                                                 | 原因                                                    | 解决                                                                                                     |
| ---------------------------------------------------- | ------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| VSCode 仍然弹窗要密码                                | `SSH_ASKPASS_REQUIRE=force` 未生效, 或 OpenSSH 版本太旧 | 检查 `ssh -V`, Windows 旧版(<8.4)不支持 `force`, 需升级 OpenSSH(设置 → 应用 → 可选功能 → OpenSSH 客户端) |
| 提示 `DISPLAY not set`                               | Windows OpenSSH 需要 `DISPLAY` 环境变量才启用 askpass   | wrapper 里已加 `DISPLAY=1`                                                                               |
| 连上后秒断                                           | `StrictHostKeyChecking` 导致交互式确认                  | SSH config 里加上 `StrictHostKeyChecking accept-new` 或 `no`                                             |
| 提示 `Bad configuration option: ssh_askpass_require` | OpenSSH 版本低于 8.4                                    | 去掉 `SSH_ASKPASS_REQUIRE`, 确保 `showLoginTerminal: false`, 让 ssh 认为自己无 tty                       |

---

## 一句话总结

**Windows**: 写一个 Go wrapper 同时充当 `ssh` 和 `SSH_ASKPASS`, VSCode 配置 `remote.SSH.path` 指向它.
**Linux**: 直接写一个输出密码的 shell 脚本作为 `SSH_ASKPASS`, 无需 wrapper, 因为环境变量注入更方便.
