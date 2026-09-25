# lazyset

把常用 TUI 放在同一個工作台：知道這台機器有哪些工具、它們用來做什麼，依工具 set 切換，也能查看 OpenSSH host 上的工具。

`lazyset` 是目前的開發名稱。它使用 Go、Bubble Tea v2、PTY 與 Charm 的 VT emulator；第一版支援 macOS / Linux，優先收錄監測工具，也包含 Git、檔案、容器與日誌工具。

## 開始使用

需要 `go.mod` 指定的 Go 1.26.5 或更新版本，以及要執行的 TUI。遠端功能使用系統的 `ssh`。

```sh
go run ./cmd/lazyset
go build -o ./bin/lazyset ./cmd/lazyset
./bin/lazyset --help
```

在 terminal 中直接執行會開啟 dashboard；stdin 或 stdout 不是 TTY 時會顯示 help，不會嘗試互動。

```sh
lazyset --set gpu
lazyset --set all
lazyset --set personal
lazyset --host gpu-box --tool nvtop
lazyset -- btop nvtop
lazyset --start-set system --start-set gpu
lazyset tools list --installed
lazyset tools list --host gpu-box --json
lazyset sets list --json
lazyset config edit
lazyset config edit --hosts
```

上面的 `lazyset` 可換成 `./bin/lazyset`。`--set` 只選擇瀏覽的 set；`--tool ID`、`-- ID...` 或 `--start-set ID` 才要求啟動工具。`--start-set` 可重複或用逗號分隔，同一工具只啟動一次。`--` 後是已設定的 **tool IDs**，不是 shell command 或傳給工具的額外參數；工具的完整 argv 仍取自 catalog/config。

啟動要求會等指定 host 的新 discovery 完成，再啟動可用的 embedded 工具，保持 Observe。不存在的 tool/set、未安裝或 external 工具會略過並保留原因；名稱警告在啟動前寫到 stderr，執行期間產生的警告在工作台退出、終端還原後輸出，避免破壞畫面。用 `:startup` 查看這次的啟動結果。`--set` 或 `--host` 的拼字錯誤仍是 usage error。單純移動游標不會啟動清單上的工具。

## 看工具與操作工具

畫面有 Hosts / Sets、工具清單、一個 terminal，以及目前的 **Observe / Interact** 狀態。作用中的 pane 使用醒目邊框，配合狀態文字辨識焦點；上方的 **Sessions N**、**Commands**、**Close** 可查看保留中的 sessions、找操作或關閉目前 session。窄畫面會收起 sidebar；候選工具仍顯示在畫面上，可用鍵盤選擇或開啟 Commands。

| 操作 | 結果 |
| --- | --- |
| Observe 中 `↑/↓` 或 `k/j` | 選候選工具、查看用途；不啟動、不影響目前程序 |
| 候選工具上按 Enter | 啟動或顯示它，保持 Observe |
| 目前執行中的工具上再按 Enter | 進入 Interact；這次 Enter 不傳给工具 |
| 點工具列 | 啟動／顯示該工具，保持 Observe |
| 點右側運行中的 terminal | 進入 Interact，預設同時把 click 交給工具 |
| 點 Interact / Back | 切換輸入擁有者；該次 click 不傳给工具 |
| 目前已退出的工具上按 Enter / 點 Reopen | 重新啟動一個新程序；該次 Enter 不傳給工具 |
| Observe 中 `/` | 篩選清單；輸入時 `q/j/k` 等都是文字 |
| Observe 中 Space 或 `:` | 開啟可搜尋的 Commands，輸入命令後按 Enter |
| Observe 中 `H` / `S` / `e` / `r` | Hosts / Sets / All（Explore）與上一個 set 切換 / refresh |
| Observe 中 `s` / `x` | Sessions / Close current session |
| Observe 中 `f` | 選擇要顯示的可用性／session 狀態 |
| Observe 中 `i` | 工具詳細資料與官方安裝指南 |
| Observe 中 `?` | 顯示快捷鍵與說明 |

Observe 時工具仍在執行。點右側 terminal 預設會把焦點與該次 click 一起交給工具；若希望第一次只聚焦，設定 `focus_click = "focus-only"`。外框與按鈕的 click 不傳給 child。Interact 中，除了 host prefix 與該工具的 `return_keys`，按鍵會交給 child。

工作台右上角 **Quit** 可用滑鼠點擊，也可 Tab 選取後按 Enter。Observe 與 Interact 都能使用；有執行中或排隊中的 session 時會先顯示確認 popup，預設選 Cancel，沒有則直接退出。

### Host prefix

預設 prefix 是 **Ctrl+\**。先按 prefix，再按下一個鍵：

Interact 底列優先顯示 **Ctrl+\ → Esc: lazyset**：依序按這兩鍵即可返回 Observe，工具保持執行；變更 prefix 後提示會一起更新。prefix → v 後的提示會明示下一鍵直接交給工具，該鍵送出後才恢復一般返回操作。

| Prefix 後的鍵 | 動作 |
| --- | --- |
| Esc | 返回 Observe，工具繼續執行 |
| `n` / `p` | 下一／上一個可用的 embedded 工具，切換後保持 Observe |
| `/` 或 `:` / `?` | Commands / help |
| `h` / `s` | Hosts / Sets |
| `x` | 關閉目前 session |
| `q` | 把真正的字元 `q` 傳给目前工具 |
| `v`，再按任意一鍵 | 把下一個鍵原樣傳給工具，略過 return key 攔截 |
| `Q` | 退出 lazyset；仍有程序時顯示確認 |
| 再按一次 prefix | 把 literal prefix 傳给工具 |

若 terminal、輸入法或外層 multiplexer 攔截 Ctrl+\，可在 config 改成 `ctrl+g`、`ctrl+space` 等。Mouse capture 也能從 Commands 暫時關閉，或設定 `mouse = false`。

常用命令：`:q` / `:quit` 退出工作台、`:close` 關閉目前 session、`:sessions` 查看 sessions、`:restart` 重啟目前工具、`:reopen` 重新開啟已退出工具、`:startup` 查看批次啟動結果、`:visibility` 選擇可見狀態、`:external` 用真實 terminal 開啟所選工具、`:config` 編輯設定、`:help` 顯示說明、`:all` 在 All 與上一個 set 間切換。Interact 中先按 prefix、`:` 開啟命令輸入，普通 `:` 仍傳給 child。

### 為什麼普通 `q` 不會關掉整個工作台

內建工具預設只攔截小寫 `q`（原生退出鍵包含 q 的工具）；translate、K9s 不設定返回鍵。Esc、Ctrl+C、F10、大寫 Q 直接交給工具，讓 dev 的 Esc 關閉 popup、lazychezmoi 的 Ctrl+C 取消對話框等操作正常運作；這些鍵也可能依工具當時狀態退出 child。在 Interact 按受保護的 q 會返回 Observe，程序繼續；要把 `q` 真正送給工具，使用 prefix、`q`。按 `i` 可查看所選工具的有效 return keys 與原生退出提示。

這是按鍵重映射，**無法分辨 child 正在搜尋、取消操作或輸入文字**：搜尋框裡受保護的 `q` 也會被攔截；若明確加入 Esc 等其他 return keys，它們在對話框中也會被攔截。必要時用 prefix、`v`、該鍵送出原鍵，或修改該工具的設定。貼上內容仍是文字，不會拆成 return keys。自訂工具預設不攔截 return keys。原生多鍵指令（如 K9s 的 `:q`）仍由 child 處理；使用者在 child 自訂的退出鍵也不會自動被偵測。

```toml
[[tools]]
id = "btop"
return_keys = [] # 不攔截任何工具按鍵；host prefix 仍可返回

[[tools]]
id = "lazychezmoi"
return_keys = ["q", "ctrl+c"] # 明確選擇額外攔截 Ctrl+C；對話框也適用
```

`return_keys` 會完整取代該工具的預設值；使用 canonical 單鍵名稱，例如 `q`、`Q`、`ctrl+c`、`esc`、`f10`。舊的 `q_to_observe` 仍可讀取，只增刪 `q`，不改其他 return keys；同一工具不能同時設定兩者。

普通 `q` 不會退出 lazyset。Child 退出後停在退出畫面；選到另一個工具再切回也不會自動重跑。對目前已退出的工具按 Enter 或 Reopen 才開新程序；Commands 也提供 Restart、Stop、Stop all，以及 Sessions。

`return_keys` 攔截的是輸入按鍵。程序終止狀態（`Cmd.Wait`）是在退出後取得，不能用來否決退出、保留已結束程序的記憶體狀態。要在真正退出前改成返回 lazyset，需要 child 配合通知；目前沒有實作這種協作協定。

**Close** 會移除該 session 的畫面與記錄；仍在執行的程序會先顯示置中的確認 popup，背景保留目前工作台，已結束的 session 直接關閉。Observe 中按 `x` 或 prefix、`x` 關閉目前 session；在 Sessions 清單按 `x` 則關閉選中的那個，避免把其他工具誤當成目標。Stop 只停止程序，保留退出畫面供查看或 Reopen。

## Catalog、sets 與可用性

內建 24 個工具。通用工具包括 `btop`、`htop`、`top`、`bottom`（執行檔為 `btm`）、`glances`、`nvtop`、`nvitop`、`gdu`、`ncdu`、`bandwhich`、`lazydocker`、`k9s`、`lnav`、`lazygit`、`yazi`、`superfile`（執行檔為 `spf`），另有以下 8 個 Personal tools。按 `i` 開啟可捲動的詳細資料；對 missing 工具按 Enter 也會開啟安裝指南。

Superfile 會出現在 **All** 與 **Files / Git**，和 Yazi 一樣預設從所選 host 的 `~` 開始；可用 `lazyset -- superfile` 預先開啟。預設保護 q，Esc／Ctrl+C／Q 仍交給工具。Superfile 的預設與 Vim keymap 不同，使用自訂按鍵時可覆寫 `return_keys`；原生 `Q` 的 shell 目錄交接無法改變 lazyset 的工作目錄。參考 [官方快捷鍵](https://superfile.dev/list/hotkey-list/)。

| Personal tools | 啟動命令 | 用途 |
| --- | --- | --- |
| dev-cli | `dev tui` | Repositories、tasks、worktrees、fleet 與 SSH hosts |
| lazychezmoi | `lazychezmoi tui` | 查看、編輯與套用 chezmoi dotfiles |
| lazyclash | `lazyclash` | Mihomo 代理目標、連線與路由 |
| lazymlflow | `lazymlflow` | MLflow experiments、runs 與 artifacts |
| lazypueue | `lazypueue` | Pueue 工作佇列與 logs |
| lazycrontab | `lazycrontab` | Cron 排程與工作變更 |
| translate | `translate` | 互動翻譯與查字 |
| exp | `exp ui` | 查看研究專案的 Git workflow 與 evidence |

這些工具沿用各自的設定與後端；lazyset 不替它們安裝或設定服務。內建 return keys 提供退出保護；文字輸入有衝突時可送出原鍵，或覆寫 `return_keys`。

內建 9 個 sets：**All**、System、GPU、Disk、Network、Containers、Logs、Files / Git、**Personal tools**（ID `personal`）。**All 就是 Explore 的同一個瀏覽入口**，自動包含所有內建與自訂工具；可從 Sets 選擇、使用 `--set all`，或設定 `default_set = "all"`。`e` 可在 All 和上一個 set 間來回，保留各自選擇與篩選。複製 All 成自訂 set 時會固定當下成員，之後新增工具只會自動加入 All。

未安裝的工具仍保留用途、狀態與官方安裝指南，幫助回想有哪些選擇；不要求裝齊所有工具，也不會自動安裝或加上 `sudo`。

All 與各 set 可透過 `f` / `:visibility` 隱藏或顯示 **Running**、**Available**、**Not installed**、**Other**。Space 勾選、`a` 全部、`o` 只選目前狀態、Enter 套用、Esc 取消。Other 保留 unknown／unsupported 的區別；不會把查詢失敗當成未安裝。可见狀態會和該 host/view 的選擇、篩選一起保存在 XDG state。

Discovery 是 **已知 catalog 加上自訂工具，對照該 host 的 PATH**。它不宣稱能認出每一個任意執行檔是不是 TUI。系統安裝與 user-wide 安裝都可以，只要在有效 PATH 上；shell aliases、functions 不會被當作執行檔。Homebrew 的 `gdu-go` 會優先辨識，GNU coreutils 的 `gdu` 不會冒充 disk-usage TUI。

- `found`：找到執行檔；不代表 GPU、Docker daemon、cluster、權限或工作目錄一定可用。
- `missing`：未找到指定執行檔。
- `unsupported`：工具設定不支援該 OS。
- `unknown`：觀測未完成或失敗，例如 SSH 認證、斷線或無法確認工具身分；不等於沒安裝。

啟動時可能先顯示帶有時間標記的 cached availability，供瀏覽參考；新的 discovery 完成前，不會依快取啟動工具。Refresh 失敗時也保留失敗狀態，需重新查詢或 Connect。

Sets 定義順序與成員；選擇 set 不會一次啟動全部工具，明確的 `--start-set` 才會提出批次啟動要求。在 Sets 畫面可新增、複製、重新命名、增加／移除／排序成員及儲存；內建 set 需複製成新 ID 才能編輯。Missing 成員不會被自動替换成另一個工具。快速 `n/p` 切換略過 missing 與 external 工具。

工具第一次開啟才啟動，切走後仍執行；同一 host + tool 在不同 set 中共用本次 session。背景程序仍可能使用 CPU。退出工作台會結束其管理的 sessions；這不是 daemon 或 detach/resume 系統。下次開啟可還原選擇與篩選，不會自動復活程序。

## 設定與自訂工具

預設不需要 config 檔案。macOS 和 Linux 都明確使用 XDG：

| 用途 | 位置 |
| --- | --- |
| 偏好與工具定義 | `$XDG_CONFIG_HOME/lazyset/config.toml`，預設 `~/.config/lazyset/config.toml` |
| 本機 hosts 與 default_host | 同目錄的 `hosts.toml`，可獨立排除在 dotfile 同步之外 |
| 選擇／篩選記錄 | `$XDG_STATE_HOME/lazyset/`，預設 `~/.local/state/lazyset/` |
| 可重建的 discovery cache | `$XDG_CACHE_HOME/lazyset/`，預設 `~/.cache/lazyset/` |

相對路徑的 XDG 環境變數會被忽略。`--config FILE` 選擇偏好檔，hosts 預設在該邏輯路徑旁的 `hosts.toml`；即使 config 是 symlink，也不會跟到 dotfiles repository 旁找 hosts。`--hosts-config FILE` 可指定另一份 hosts 檔。不會默默載入目前目錄的 config。

```sh
lazyset config path
lazyset config path --hosts
lazyset config show --json
lazyset config validate
lazyset --config ./examples/config.toml config validate
lazyset config edit
lazyset config edit --hosts
lazyset --hosts-config ~/.config/lazyset/work-hosts.toml config show --json
```

`config show` 回傳 `path`、`hosts_path` 與合併設定，JSON 與人類輸出都遮蔽所有 `env` 值。`config edit` 依序使用 `$VISUAL`、`$EDITOR`、`vi`；支援引號參數，不做 shell evaluation。`config edit --hosts` 只編輯 hosts 檔，完成後仍驗證兩份檔案的合併結果。路徑與設定檢查不會建立檔案；實際 edit 或 UI 儲存 set / host 時才寫入。既有 TOML 即使錯誤也能開啟修復；無效的使用者修改會保留。

舊 config 裡的 `default_host`／`[[hosts]]` 仍能讀取，並提示搬到 hosts 檔；不會自動改寫使用者檔案。hosts 檔的同 ID 記錄會完整取代舊記錄，`default_host` 也由 hosts 檔優先。省略的預設檔可不存在；明確指定 `--config`／`--hosts-config` 的檔案不存在則報錯（edit 可以建立它）。

完整範例見 [examples/config.toml](examples/config.toml) 與 [examples/hosts.toml](examples/hosts.toml)。偏好與工具放 config.toml：

```toml
prefix = 'ctrl+\'
mouse = true
focus_click = "forward"
default_set = "all"

[[tools]]
id = "editor"
name = "Neovim"
description = "Edit files with my regular Neovim configuration"
command = ["nvim"]
mode = "external"
return_keys = []

[[sets]]
id = "my-workbench"
name = "My workbench"
tools = ["btop", "nvtop", "lazygit", "editor"]
```

本機目標放 hosts.toml：

```toml
default_host = "local"

[[hosts]]
id = "gpu-box"
name = "GPU server"
ssh = "gpu-box" # ~/.ssh/config 中的 alias
```

`command` 是 executable + args 陣列，不是 shell script。可另外設定 `dir`、`env`、`mode`、`return_keys`、`description` 等；同名內建 tool 的省略欄位繼承原設定，新 tool 的 return keys 預設為空。

**Yazi 預設 `dir = "~"`，會在目前 host 的 HOME 開啟。** `lazygit` 保留啟動 lazyset 時的 repository 目錄。其他未指定 `dir` 的工具在本機使用啟動目錄，SSH 使用遠端登入目錄；遠端 Git 工具通常要指定 repository。`dir = "~"` 或 `"~/path"` 會依目標 host 的 HOME 展開，也可用絕對路徑；覆寫 Yazi 的 `dir = ""` 可回到一般預設目錄。

UI 把 custom set 寫入 config、host 寫入 hosts 檔，保留其他設定與註解；檔案已被另一個 editor 修改時會拒絕覆蓋。UI 使用 `[[sets]]` / `[[hosts]]` 表格；手寫 inline `sets = [{...}]` 或 `hosts = [{...}]` 可讀取，但需透過對應的 config editor 修改。

## SSH 與外部全螢幕模式

在 **Hosts** 清單按 `n`，或執行 `:host-add`，即可輸入 OpenSSH alias（也接受 `user@host`）與可選顯示名稱；**Ctrl+S 只儲存設定，不會自動連線或探索新主機**。之後選擇該 host 才進行 discovery，需要認證時使用 Connect。Esc 取消新增。

每個 host 獨立做 discovery；不會自動連線所有已知機器。使用 OpenSSH aliases，可沿用 `~/.ssh/config` 的 identity、ProxyJump 與連線共用設定。背景檢查不會詢問密碼或接受未知 host key；需要認證時，在 dashboard 使用 **Connect**，暫時交還 terminal 給原生 SSH 處理密碼、MFA 或 trust，再回來重查。

Remote 非互動 shell 的 PATH 可能與平常登入時不同。可在該 host 的 `env` 設定完整 PATH，或在 tool 指定絕對 executable path。不要把 SSH 密碼放進 config。Host 切換保留各自 session，terminal 上的 target label 顯示實際執行位置；斷線不會被當成「工具不存在」，也不會自動重跑工具。

不是所有 terminal 功能都能完整嵌套。Terminal-specific 圖片、clipboard、特殊 keyboard protocol、外開程式、另一層 multiplexer 或極窄畫面都可能需要 **external** mode。透過 config 為工具設定 `mode = "external"`。此時整個 terminal 交給 child，工具自己的按鍵負責退出，結束後返回工作台；外層無法提供 Observe 或攔 `q`。

也可使用 `:external` 單次在真實 terminal 開啟所選工具，不改預設 embedded 模式。同一 host/tool 若已有運行中的嵌入 session，先 Stop 或 Close；此操作不會搬移或複製現有程序。

**dev-cli 保持 embedded。** dev 在 repo/task 上按 Enter 會先退出自己的 dashboard，再啟用 Herdr／tmux 等 runtime。在外層 Herdr 中可能切走該 workspace；沒有外層 runtime 時可能在 child PTY 裡接上新 client，detach 後 dev 才結束。lazyset 返回後保留退出畫面，按 Reopen／Enter 可重新開啟 dev。這是新程序，不保證保留 dev 內部選擇，也不能修改 lazyset 父 shell 的 cwd；不會自動重播 repo Enter、推測導航成功或用 wrapper 不斷重啟。

## 開發與驗證

```sh
go test ./...
go test -race ./...
go vet ./...
go build -o ./bin/lazyset ./cmd/lazyset
python3 scripts/pty_smoke.py bin/lazyset --real-monitors --real-lazychezmoi --real-dev --real-superfile
```

測試涵蓋設定合併／保留註解／並行修改、CLI 的 JSON 與 TTY 意圖、discovery／SSH 命令、session lifecycle 與輸入路由。PTY smoke 使用隔離設定，驗證 prefix、q、paste、mouse、resize、editor handoff 與退出後 terminal restoration；`--real-monitors` 另外測試已安裝的 btop／htop，`--real-lazychezmoi`、`--real-dev` 與 `--real-superfile` 使用隔離設定檢查原生對話框按鍵。

目前已在 macOS 真實 PTY 驗證 btop／htop／lazychezmoi／dev／Superfile。lazychezmoi 使用隔離的來源、目的目錄與設定；dev 使用暫存路徑並停用 runtime handoff；Superfile 使用暫存 HOME／XDG 目錄並停用更新檢查與預覽。已確認 dev 與 Superfile 的 Esc 關閉 Help、lazychezmoi 的 Ctrl+C 取消 Actions 後仍在 Interact，q 與 prefix → Esc 返回 Observe 並保留 PID，以及滑鼠 Quit 的取消、確認與終端還原。Superfile 也驗證原生退出後保留退出畫面、明確 Reopen 才重開。Linux 完成交叉編譯，尚未在 Linux terminal 或真實 SSH host 驗證。模擬 SSH 測試不能取代實際主機上的認證、環境與終端相容性檢查。

目前是從 source checkout 建置的實驗版，尚無公開 release 或受支援的升級頻道，因此不提供 `upgrade`；更新 checkout 後重新執行 build。`version` 預設顯示 `dev`，可用 `go build -ldflags '-X main.version=YOUR_VERSION' -o ./bin/lazyset ./cmd/lazyset` 指定建置版本。
