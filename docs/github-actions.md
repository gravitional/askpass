为 GitHub 开源库配置跨平台 CI 流水线, 核心思路是 **GitHub Actions + Matrix 矩阵策略**:
用一份 Workflow 定义, 并行触发多个不同 OS/架构的 Job, 编译完成后自动打包并发布到 Release.
下面按「通用模板 → 具体语言示例 → 发布自动化 → 最佳实践」展开.

---

## 1. 基础多平台构建(Matrix 策略)

在 `.github/workflows/build.yml` 中, 用 `strategy.matrix` 同时定义操作系统和编译目标, GitHub Actions 会自动并行调度.

```yaml
name: Multi-Platform Build

on:
  push:
    branches: [main]
    tags: ['v*']
  pull_request:
    branches: [main]

jobs:
  build:
    strategy:
      matrix:
        include:
          - os: ubuntu-latest
            target: linux-x64
            ext: ''
          - os: macos-latest
            target: darwin-x64
            ext: ''
          - os: macos-14        # Apple Silicon
            target: darwin-arm64
            ext: ''
          - os: windows-latest
            target: win-x64
            ext: '.exe'

    runs-on: ${{ matrix.os }}

    steps:
      - name: Checkout
        uses: actions/checkout@v4

      # 根据你的语言选择对应的 setup action
      # 示例: Node.js
      - name: Setup Node.js
        uses: actions/setup-node@v4
        with:
          node-version: '20'
          cache: 'npm'

      - name: Install dependencies
        run: npm ci

      - name: Build for ${{ matrix.target }}
        run: npm run build -- --target=${{ matrix.target }}

      - name: Upload artifact
        uses: actions/upload-artifact@v4
        with:
          name: myapp-${{ matrix.target }}
          path: dist/myapp${{ matrix.ext }}
          retention-days: 5
```

Matrix 策略会为每个 `include` 项创建一个独立 Job, 在对应 runner 上并行执行 . 公共仓库使用免费额度不限时长, 私有仓库每月 2,000 分钟免费 .

---

## 2. 分阶段流水线: 先测试, 后构建, 再发布

生产级配置应把「测试」和「构建」拆成依赖 Job, 避免测试失败还浪费多平台编译资源:

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with: { node-version: '20', cache: 'npm' }
      - run: npm ci
      - run: npm test

  build:
    needs: test          # 只有 test 通过才执行
    strategy:
      matrix:
        include:
          - os: ubuntu-latest
            target: linux-x64
          - os: macos-latest
            target: darwin-x64
          - os: macos-14
            target: darwin-arm64
          - os: windows-latest
            target: win-x64
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with: { node-version: '20', cache: 'npm' }
      - run: npm ci
      - run: npm run build -- --target=${{ matrix.target }}
      - uses: actions/upload-artifact@v4
        with:
          name: build-${{ matrix.target }}
          path: dist/

  release:
    needs: build
    runs-on: ubuntu-latest
    permissions:
      contents: write    # 允许创建 Release
    steps:
      - uses: actions/download-artifact@v4
        with:
          path: artifacts/
          merge-multiple: true

      - name: Create archives
        run: |
          cd artifacts
          for d in */; do
            base=$(basename "$d")
            if [[ "$base" == *"windows"* ]]; then
              zip -r "../${base}.zip" "$d"
            else
              tar -czvf "../${base}.tar.gz" "$d"
            fi
          done
          cd ..
          sha256sum *.tar.gz *.zip > checksums.txt

      - name: Release
        uses: softprops/action-gh-release@v2
        if: startsWith(github.ref, 'refs/tags/')
        with:
          files: |
            *.tar.gz
            *.zip
            checksums.txt
```

这种「测试 → 构建 → 发布」的依赖链能显著节省计算资源, 且发布步骤只在打 tag 时触发 .

---

## 3. 按语言/工具链的针对性配置

### Rust(原生跨平台二进制)

```yaml
jobs:
  build:
    strategy:
      matrix:
        include:
          - os: ubuntu-latest
            target: x86_64-unknown-linux-gnu
          - os: macos-latest
            target: x86_64-apple-darwin
          - os: macos-14
            target: aarch64-apple-darwin
          - os: windows-latest
            target: x86_64-pc-windows-msvc
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: dtolnay/rust-toolchain@stable
        with:
          targets: ${{ matrix.target }}
      - run: cargo build --release --target ${{ matrix.target }}
      - uses: actions/upload-artifact@v4
        with:
          name: myapp-${{ matrix.target }}
          path: target/${{ matrix.target }}/release/myapp*
```

### Go(内置交叉编译, 单平台可出多架构)

Go 的交叉编译特性让你可以在 `ubuntu-latest` 上一次性编译所有平台, 无需多 OS runner:

```yaml
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        include:
          - goos: linux
            goarch: amd64
          - goos: linux
            goarch: arm64
          - goos: darwin
            goarch: amd64
          - goos: darwin
            goarch: arm64
          - goos: windows
            goarch: amd64
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - env:
          GOOS: ${{ matrix.goos }}
          GOARCH: ${{ matrix.goarch }}
        run: |
          output="myapp-${{ matrix.goos }}-${{ matrix.goarch }}"
          [ "$GOOS" = "windows" ] && output="${output}.exe"
          go build -ldflags="-s -w" -o "$output" ./cmd/myapp
      - uses: actions/upload-artifact@v4
        with:
          name: ${{ matrix.goos }}-${{ matrix.goarch }}
          path: myapp-*
```

### Python(Wheel 多平台打包)

如需发布 C 扩展的 wheel, 可用 `cibuildwheel`:

```yaml
jobs:
  build-wheels:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: pypa/cibuildwheel@v2.19
      - uses: actions/upload-artifact@v4
        with:
          name: wheels-${{ matrix.os }}
          path: wheelhouse/*.whl
```

---

## 4. 自动发布到 GitHub Releases

打 tag 后自动创建 Release 并上传产物, 是开源库分发二进制/安装包的标准做法:

```yaml
  release:
    needs: build
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/download-artifact@v4
        with:
          path: artifacts/
          merge-multiple: false

      - name: Repackage
        run: |
          mkdir -p release
          for dir in artifacts/*/; do
            name=$(basename "$dir")
            if [[ "$name" == *"windows"* ]] || [[ "$name" == *"win"* ]]; then
              (cd "$dir" && zip -r "../../release/${name}.zip" .)
            else
              tar -czvf "release/${name}.tar.gz" -C "$dir" .
            fi
          done
          cd release && sha256sum * > ../checksums.txt

      - name: Publish Release
        uses: softprops/action-gh-release@v2
        with:
          draft: false
          generate_release_notes: true
          files: |
            release/*
            checksums.txt
```

建议同时生成 SHA256 校验文件, 方便用户验证下载完整性 .

---

## 5. 最佳实践与优化

| 优化项 | 做法 |
|--------|------|
| **缓存依赖** | 使用 `actions/setup-Xxx` 内置的 `cache` 参数, 或 `actions/cache@v4` 缓存 `node_modules`, `target/`, `~/.cargo` 等, 可减少 40–60% 构建时间  |
| **并发控制** | 在 workflow 顶层加 `concurrency`, 避免快速 push 导致队列堆积: <br>`concurrency: { group: ci-${{ github.ref }}, cancel-in-progress: true }` |
| **权限最小化** | 给 Job 显式声明 `permissions`, 如 `contents: write` 仅给 release job, 其余默认只读 |
| **Apple Silicon** | macOS ARM64 用 `macos-14`(或更高), x86_64 用 `macos-latest`(目前仍为 Intel 或 Rosetta) |
| **Release Draft** | 重要版本建议先创建 `draft: true` 的 Release, 人工审核后再发布  |
| **语义化版本** | 配合 `tags: ['v*']` 触发, 遵循 SemVer, 便于自动化变更日志生成 |

---

## 6. 快速开始模板

如果你只想「先跑起来」, 把下面这段保存为 `.github/workflows/release.yml`, 替换构建命令即可:

```yaml
name: Release

on:
  push:
    tags: ['v*']

permissions:
  contents: write

jobs:
  build:
    strategy:
      matrix:
        include:
          - os: ubuntu-latest
            target: linux-amd64
          - os: macos-latest
            target: darwin-amd64
          - os: macos-14
            target: darwin-arm64
          - os: windows-latest
            target: windows-amd64
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      # ===== 替换为你的编译步骤 =====
      - run: echo "Build your project here for ${{ matrix.target }}"
      # ==============================
      - uses: actions/upload-artifact@v4
        with:
          name: ${{ matrix.target }}
          path: dist/*          # 替换为你的产物路径

  release:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/download-artifact@v4
        with:
          path: artifacts/
          merge-multiple: true
      - uses: softprops/action-gh-release@v2
        with:
          generate_release_notes: true
          files: artifacts/**/*
```

推送到仓库后, 打一个 tag(如 `git tag v0.1.0 && git push origin v0.1.0`), GitHub Actions 就会自动编译全平台包并创建 Release.