---
name: Build executable name
description: Always build the Go project as tsw-hud.exe, not hud-go.exe
type: feedback
---

Always build the hud-go project as `tsw-hud.exe`:
```
go build -o tsw-hud.exe .
```

**Why:** The server runs as `tsw-hud.exe`. Building with any other name means the user restarts the old binary and changes don't take effect.

**How to apply:** Any time you need to rebuild the hud-go project, use `-o tsw-hud.exe`.
