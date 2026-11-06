# PostGo

用 Go 写的跨平台 API 客户端（macOS / Linux / Windows），界面基于 [MyGo](https://github.com/egoist/mygo) 的 Native UI。

- 设计方案：[docs/DESIGN.md](docs/DESIGN.md)
- 集合文件格式：[docs/FORMAT.md](docs/FORMAT.md)

```sh
go test -race ./...        # 测试
go run ./tools/checkdeps   # 检查 core 和 CLI 不依赖 MyGo
```

需要 Go 1.27.3 或更高版本。
