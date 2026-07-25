# Third-Party Notices

Metadata Service uses the following Go modules. Exact versions are locked in `go.mod` and `go.sum`; the corresponding license texts are copied without modification under `LICENSES/`.

| Module | Version | License material |
|---|---|---|
| `github.com/gorilla/mux` | `v1.8.1` | `LICENSES/gorilla-mux.txt` |
| `github.com/gorilla/websocket` | `v1.5.3` | `LICENSES/gorilla-websocket.txt` |
| `github.com/sirupsen/logrus` | `v1.9.4` | `LICENSES/logrus.txt` |
| `golang.org/x/sys` | `v0.13.0` | `LICENSES/golang-x-sys.txt` |
| `gopkg.in/yaml.v2` | `v2.4.0` | `LICENSES/yaml-v2.txt`, `LICENSES/yaml-v2-libyaml.txt`, `LICENSES/yaml-v2-NOTICE.txt` |

The former event behavior was reviewed against Apache-2.0-licensed sources from `rancher/event-subscriber` at commit `cdcd1659ec46128cf3118ae6c56ade83b220ff79` and `rancher/go-rancher` at commit `f0378de1178a553cfb64666c0281486f593f0f05`. Their large generated clients are not included in the current tree; the replacement implements only WebSocket subscription and reply publication required by this service.

The root [LICENSE](LICENSE) continues to govern inherited project code. This notice does not replace any dependency license text.
