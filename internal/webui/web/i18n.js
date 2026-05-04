// i18n.js — small translation layer for the duckllo UI.
//
//  - User picks a language via the topbar dropdown.
//  - Choice persists in localStorage under "duckllo.lang".
//  - `t(key)` looks up the string for the current language; falls back
//    to the `en` table, then to the key itself (so a missing string is
//    visible rather than blank).
//  - On change we fire a `langchange` window event; pages that should
//    re-render on language switch listen for it.
//
// Keep keys flat and grouped by page (`spec.*`, `run.*`, …); template
// substitution uses `{name}` placeholders replaced by `t(key, {name:…})`.

const STORAGE_KEY = "duckllo.lang";
const DEFAULT_LANG = "en";
export const LANGS = [
  { code: "en",    label: "English",          short: "EN" },
  { code: "zh-TW", label: "繁體中文",          short: "中" },
];

const STRINGS = {
  en: {
    "nav.signin":     "Sign in",
    "nav.logout":     "logout",
    "nav.newProject": "+ New project",

    "login.title":    "Sign in",
    "login.username": "Username",
    "login.password": "Password",
    "login.submit":   "Sign in",
    "login.register": "or register a new account",

    "specs.title":            "Specs",
    "specs.filter.all":       "All",
    "specs.btn.steering":     "Steering",
    "specs.btn.steeringHelp": "Steering: edit the project's harness rules — guides the runner concatenates into every iteration's prompt — and review recurring failure patterns. This is where you teach the harness 'don't make this mistake again' instead of correcting it inline each time.",
    "specs.btn.newSpec":      "New spec",
    "specs.btn.newSpecHelp":  "Compose a new spec: title, intent, and typed acceptance criteria. Approving the spec freezes the criteria and unlocks 'Start run'.",
    "specs.empty":            "No specs yet. Create one to start.",

    "specNew.title":              "New spec",
    "specNew.subtitle":           "Each acceptance criterion is a typed sensor target — the runner reads sensor_kind to decide which sensor fires.",
    "specNew.field.title":        "Title",
    "specNew.field.titleHelp":    "Short imperative-voice summary of the change (≤ 70 chars). Becomes the spec's headline in the project list. Frozen once the spec is approved.",
    "specNew.field.intent":       "Intent",
    "specNew.field.intentHelp":   "Why this matters and what success looks like. 2-4 sentences is plenty — focus on the user-visible goal, the constraint, and the win condition. The runner reads this on every iteration; the LLM judge uses it to decide if the diff matches intent. Frozen on approval.",
    "specNew.field.priority":     "Priority",
    "specNew.field.priorityHelp": "Sort order on the project's spec list. Doesn't change runner behaviour — purely organisational.",
    "specNew.criteria":           "Acceptance criteria",
    "specNew.criteriaHelp":       "Each criterion is a typed sensor target — when the runner enters the validate phase, it fires one sensor per criterion (lint runs golangci-lint, screenshot drives chromedp, judge calls the LLM judge…). A run only reaches 'done' when every non-manual criterion has a passing verification.",
    "specNew.btn.suggest":        "Suggest from title + intent",
    "specNew.btn.suggestBusy":    "Asking the model…",
    "specNew.btn.refining":       "Refining…",
    "specNew.btn.addCriterion":   "Add criterion",
    "specNew.btn.create":         "Create",
    "specNew.btn.cancel":         "Cancel",
    "specNew.refine.title":       "Refined draft + clarifying questions",
    "specNew.refine.help":        "Edit the refined fields if you like, answer the questions (click a chip or type), then generate. Generating also writes the refined title + intent back to the form above.",
    "specNew.refine.refTitle":    "Refined title",
    "specNew.refine.refIntent":   "Refined intent",
    "specNew.refine.questions":   "Questions",
    "specNew.refine.empty":       "The model didn't have any clarifying questions — go ahead and generate criteria.",
    "specNew.refine.apply":       "Apply refined draft + generate criteria",
    "specNew.refine.dismiss":     "Dismiss",

    "run.statusLine":      "status={status} · turns={used}/{budget} · tokens={tokens}",
    "run.btn.abort":       "Abort run",
    "run.btn.complete":    "Mark complete",
    "run.btn.preview":     "Preview prompt",
    "run.btn.previewHelp": "Show the assembled prompt the agent sees at each phase, with each segment labeled by its source so you can trace and edit it.",
    "run.btn.backToSpec":  "Back to spec",

    "preview.title":     "Prompt preview",
    "preview.subtitle":  "What the agent sees if it claims this phase right now. Each block is tagged with its source — click 'Edit source' to jump to the page that controls that text.",
    "preview.btn.refresh": "Refresh",
    "preview.btn.refreshHelp": "Re-fetch the assembled prompt — useful after editing a source document",
    "preview.btn.back":  "Back to run",
    "preview.systemHeading": "System prompt",
    "preview.userHeading":   "User message — labeled segments",
    "preview.editSource":    "Edit source →",
    "preview.empty":         "No user-message content for this phase.",

    "steering.title":      "Steering loop — {project}",
    "steering.subtitle":   "Edit the guides and rules the runner bakes into every iteration's prompt. Issues that recur are a signal to encode a new rule here rather than retry.",
    "steering.tab.rules":  "Harness rules",
    "steering.tab.topo":   "Topologies",
    "steering.tab.fails":  "Recurring failures",
    "steering.tab.keys":   "API keys",
    "steering.btn.back":   "Back to specs",
    "steering.lang":       "Agent language",
    "steering.langHelp":   "What language the agent's human-readable replies (plan summaries, judge verdicts, criterion analyses) come back in. JSON keys and enum values stay English so the parser never breaks. Settable per project.",

    "bar.allClear":  "all clear",
    "bar.specs":     "specs",
    "bar.runs":      "runs",
    "bar.alerts":    "alerts",
    "bar.draft":     "draft",
    "bar.proposed":  "proposed",
    "bar.approved":  "approved",
    "bar.running":   "running",
    "bar.validated": "validated",
    "bar.active":    "active",
    "bar.reviewing": "reviewing",
    "bar.correcting":"correcting",
    "bar.openAnnotations": "open annotations",
  },

  "zh-TW": {
    "nav.signin":     "登入",
    "nav.logout":     "登出",
    "nav.newProject": "+ 新增專案",

    "login.title":    "登入",
    "login.username": "使用者名稱",
    "login.password": "密碼",
    "login.submit":   "登入",
    "login.register": "或註冊新帳號",

    "specs.title":            "規格",
    "specs.filter.all":       "全部",
    "specs.btn.steering":     "操控",
    "specs.btn.steeringHelp": "操控：編輯本專案的 harness 規則 — runner 會把這些規則串接進每一次 iteration 的 prompt — 並檢視重複出現的失敗模式。在這裡把「不要再犯同樣的錯」教給 harness，而不是每次都靠人工修正。",
    "specs.btn.newSpec":      "新增規格",
    "specs.btn.newSpecHelp":  "撰寫一份新規格：標題、意圖、以及帶有 sensor 類型的驗收條件。核准規格後條件即被凍結，「開始執行」按鈕也會解鎖。",
    "specs.empty":            "尚無規格。建立一份開始吧。",

    "specNew.title":              "新增規格",
    "specNew.subtitle":           "每個驗收條件都是一個有類型的 sensor 目標 — runner 會根據 sensor_kind 決定要觸發哪個 sensor。",
    "specNew.field.title":        "標題",
    "specNew.field.titleHelp":    "用祈使句寫出簡潔的變更摘要（≤ 70 字元）。會作為規格在專案列表中的標頭。規格被核准後即凍結。",
    "specNew.field.intent":       "意圖",
    "specNew.field.intentHelp":   "為什麼這件事重要、成功是什麼樣子。寫 2-4 句就夠了 — 聚焦在使用者可見的目標、限制條件、以及成功判斷。Runner 每次 iteration 都會讀這段；LLM judge 會用它判斷 diff 是否符合意圖。核准後凍結。",
    "specNew.field.priority":     "優先順序",
    "specNew.field.priorityHelp": "在專案規格列表中的排序。不會改變 runner 的行為 — 純粹是組織用途。",
    "specNew.criteria":           "驗收條件",
    "specNew.criteriaHelp":       "每個條件是一個有類型的 sensor 目標 — runner 進入 validate 階段時會為每個條件觸發一個 sensor（lint 跑 golangci-lint，screenshot 透過 chromedp，judge 呼叫 LLM judge…）。每個非 manual 的條件都拿到 pass verification 後，這個 run 才會走到 done。",
    "specNew.btn.suggest":        "從標題與意圖建議",
    "specNew.btn.suggestBusy":    "等模型回覆中…",
    "specNew.btn.refining":       "精煉中…",
    "specNew.btn.addCriterion":   "新增條件",
    "specNew.btn.create":         "建立",
    "specNew.btn.cancel":         "取消",
    "specNew.refine.title":       "精煉草稿與澄清問題",
    "specNew.refine.help":        "需要的話編輯精煉後的欄位，回答問題（點擊選項或自行輸入），然後產生條件。產生時也會把精煉後的標題與意圖寫回上方表單。",
    "specNew.refine.refTitle":    "精煉後標題",
    "specNew.refine.refIntent":   "精煉後意圖",
    "specNew.refine.questions":   "問題",
    "specNew.refine.empty":       "模型沒有任何澄清問題 — 直接產生條件吧。",
    "specNew.refine.apply":       "套用精煉草稿並產生條件",
    "specNew.refine.dismiss":     "關閉",

    "run.statusLine":      "狀態={status} · 回合={used}/{budget} · tokens={tokens}",
    "run.btn.abort":       "中止執行",
    "run.btn.complete":    "標記完成",
    "run.btn.preview":     "預覽 prompt",
    "run.btn.previewHelp": "顯示 agent 在每個階段會看到的完整 prompt，每段內容都會標示來源，方便追蹤和編輯。",
    "run.btn.backToSpec":  "回到規格",

    "preview.title":     "Prompt 預覽",
    "preview.subtitle":  "如果現在 agent 認領這個階段，它會看到這些內容。每個區塊都標示了來源 — 點「編輯來源」可以跳到能修改該段內容的頁面。",
    "preview.btn.refresh": "重新整理",
    "preview.btn.refreshHelp": "重新抓取組裝後的 prompt — 編輯來源文件後很有用",
    "preview.btn.back":  "回到執行",
    "preview.systemHeading": "系統 prompt",
    "preview.userHeading":   "使用者訊息 — 標籤分段",
    "preview.editSource":    "編輯來源 →",
    "preview.empty":         "此階段沒有使用者訊息內容。",

    "steering.title":      "操控 — {project}",
    "steering.subtitle":   "編輯 runner 在每次 iteration 的 prompt 中加入的指南與規則。重複出現的問題就把它編成一條規則，不要每次手動修正。",
    "steering.tab.rules":  "Harness 規則",
    "steering.tab.topo":   "Topologies",
    "steering.tab.fails":  "重複失敗",
    "steering.tab.keys":   "API 金鑰",
    "steering.btn.back":   "回到規格",
    "steering.lang":       "Agent 語言",
    "steering.langHelp":   "Agent 的人類可讀回覆（計畫摘要、judge 判決、條件分析）會用什麼語言回應。JSON 的 key 與 enum 值仍維持英文，這樣 parser 不會壞掉。可依專案設定。",

    "bar.allClear":  "無待辦",
    "bar.specs":     "規格",
    "bar.runs":      "執行",
    "bar.alerts":    "警示",
    "bar.draft":     "草稿",
    "bar.proposed":  "提議",
    "bar.approved":  "已核准",
    "bar.running":   "執行中",
    "bar.validated": "已驗證",
    "bar.active":    "進行中",
    "bar.reviewing": "待審查",
    "bar.correcting":"修正中",
    "bar.openAnnotations": "開放中的註解",
  },
};

let currentLang = (localStorage.getItem(STORAGE_KEY) || DEFAULT_LANG);
if (!STRINGS[currentLang]) currentLang = DEFAULT_LANG;

export function getLang() { return currentLang; }

export function setLang(code) {
  if (!STRINGS[code]) return;
  if (code === currentLang) return;
  currentLang = code;
  localStorage.setItem(STORAGE_KEY, code);
  window.dispatchEvent(new Event("langchange"));
}

// t(key) returns the translated string for the current language.
// vars supplies {placeholder} substitutions, e.g. t("run.statusLine",
// {status:"queued", used:0, budget:50, tokens:0}).
export function t(key, vars) {
  const tbl = STRINGS[currentLang] || STRINGS[DEFAULT_LANG];
  let s = tbl[key];
  if (s === undefined) s = STRINGS[DEFAULT_LANG][key];
  if (s === undefined) s = key;
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (_, k) => (k in vars ? String(vars[k]) : `{${k}}`));
}
