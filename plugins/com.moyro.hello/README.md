# Hello plugin fixture

This directory intentionally tracks the manifest only. Native plugin binaries
are trusted executable code and are not committed as opaque build artifacts.

Build the Windows fixture from the reviewed SDK example when it is needed:

```bash
cd plugin-sdk/go
GOTOOLCHAIN=local GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -buildvcs=false -ldflags="-s -w" \
  -o ../../plugins/com.moyro.hello/server/plugin-windows-amd64.exe \
  ./example/hello
```

Review the source and checksum the resulting binary before provisioning it to
an operator-controlled plugin directory. v0.2.9 does not sandbox plugins.
