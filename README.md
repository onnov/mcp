# mcp

## Сборка

```sh
CGO_ENABLED=0 go build -ldflags="-s -w" -o github_public_mcp main.go
```

## Ручной деплой
```sh
scp github_public_mcp s1169074@176.32.38.41:/home/s1169074/mcp
```