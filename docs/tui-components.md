# TUI component consumers

| Component | Consumer | Notes |
| --- | --- | --- |
| Tabs | In-TUI Settings | Sections: Forge, GitHub, Proxy, Embedder, Voyage, Appearance, Environment, Host. |

The settings screen keeps its section list in `internal/tui/settings/model.go`; it
uses the application-wide visual component grammar when linked into a themed TUI.
