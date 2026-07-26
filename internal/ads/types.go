package ads

const (
	// FetchURL 保留旧名称表示默认广告位地址。
	FetchURL = "https://ads.leokun.cn/1"
	// FetchURL     = "http://localhost:3000/ad.zip"
	RoutePrefix  = "/ad"
	EventUpdated = "ad:updated"

	// Enabled 是广告模块的总开关。
	//
	// 置为 false 后：不拉取远端包、不注册前端 service、不上报设备与用量指纹。
	// 保留整套实现而非删除，是为了后续与上游合并时不产生持续冲突。
	Enabled = false
)

type Slot struct {
	ID       string
	FetchURL string
}

var builtinSlots = []Slot{
	{ID: "1", FetchURL: "https://ads.leokun.cn/1"},
	{ID: "2", FetchURL: "https://ads.leokun.cn/2"},
	{ID: "3", FetchURL: "https://ads.leokun.cn/3"},
}

// Slots 返回当前生效的广告位；总开关关闭时为空，所有遍历自然空转。
func Slots() []Slot {
	if !Enabled {
		return nil
	}
	return builtinSlots
}

type WindowConfig struct {
	Width  int `json:"width" yaml:"width"`
	Height int `json:"height" yaml:"height"`
}

type HomePlacementConfig struct {
	Title    string `json:"title" yaml:"title"`
	Subtitle string `json:"subtitle" yaml:"subtitle"`
}

type Config struct {
	Enabled bool                `json:"enabled" yaml:"enabled"`
	Window  WindowConfig        `json:"window" yaml:"window"`
	Home    HomePlacementConfig `json:"home" yaml:"home"`
}

type Runtime struct {
	Available    bool                `json:"available"`
	Enabled      bool                `json:"enabled"`
	PackageHash  string              `json:"packageHash"`
	AssetBaseURL string              `json:"assetBaseURL"`
	IndexURL     string              `json:"indexURL"`
	Window       WindowConfig        `json:"window"`
	Home         HomePlacementConfig `json:"home"`
	Slots        []SlotRuntime       `json:"slots"`
}

type SlotRuntime struct {
	ID           string              `json:"id"`
	Available    bool                `json:"available"`
	Enabled      bool                `json:"enabled"`
	PackageHash  string              `json:"packageHash"`
	AssetBaseURL string              `json:"assetBaseURL"`
	IndexURL     string              `json:"indexURL"`
	Window       WindowConfig        `json:"window"`
	Home         HomePlacementConfig `json:"home"`
}

type MetricsSnapshot struct {
	TurnsTotal         int
	RequestTokensTotal int64
	PromptTokensTotal  int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
}

type FetchResult struct {
	Hash    string
	Changed bool
}
