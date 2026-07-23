package voice

import "strings"

const (
	defaultVoice        = "cove"
	defaultVoiceMode    = "wingman"
	defaultLanguageCode = "auto"
)

// Choice is one public identifier and display label.
type Choice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// VoiceChoice describes one upstream realtime voice.
type VoiceChoice struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// LanguageChoice provides stable labels without tying downstream clients to
// the built-in page's current locale.
type LanguageChoice struct {
	ID     string `json:"id"`
	NameZH string `json:"name_zh"`
	NameEN string `json:"name_en"`
}

// SessionDefaults are the values applied when a downstream field is omitted.
type SessionDefaults struct {
	Voice        string `json:"voice"`
	VoiceMode    string `json:"voice_mode"`
	LanguageCode string `json:"language_code"`
}

// DataChannelConfig is the negotiated channel required by ChatGPT voice.
type DataChannelConfig struct {
	Label      string `json:"label"`
	Negotiated bool   `json:"negotiated"`
	ID         int    `json:"id"`
}

// ICEServerConfig is compatible with the browser RTCIceServer shape.
type ICEServerConfig struct {
	URLs []string `json:"urls"`
}

// WebRTCConfig contains connection bootstrap values safe for downstream use.
type WebRTCConfig struct {
	DataChannel  DataChannelConfig `json:"data_channel"`
	ICEServers   []ICEServerConfig `json:"ice_servers"`
	ReceiveAudio bool              `json:"receive_audio"`
	ReceiveVideo bool              `json:"receive_video"`
}

// PublicConfig is the complete non-secret downstream capability document.
type PublicConfig struct {
	Version    string           `json:"version"`
	Defaults   SessionDefaults  `json:"defaults"`
	Voices     []VoiceChoice    `json:"voices"`
	VoiceModes []Choice         `json:"voice_modes"`
	Languages  []LanguageChoice `json:"languages"`
	WebRTC     WebRTCConfig     `json:"webrtc"`
}

var voiceChoices = []VoiceChoice{
	{ID: "breeze", Name: "Breeze", Description: "Animated"},
	{ID: "cove", Name: "Cove", Description: "Composed"},
	{ID: "ember", Name: "Ember", Description: "Confident"},
	{ID: "fathom", Name: "Arbor", Description: "Easygoing"},
	{ID: "glimmer", Name: "Sol", Description: "Savvy"},
	{ID: "juniper", Name: "Juniper", Description: "Open"},
	{ID: "maple", Name: "Maple", Description: "Cheerful"},
	{ID: "orbit", Name: "Spruce", Description: "Calm"},
	{ID: "vale", Name: "Vale", Description: "Bright"},
}

var voiceModeChoices = []Choice{
	{ID: defaultVoiceMode, Name: "Standard"},
}

var languageChoices = []LanguageChoice{
	{ID: "auto", NameZH: "自动检测", NameEN: "Auto-detect"},
	{ID: "en", NameZH: "英语", NameEN: "English"},
	{ID: "es", NameZH: "西班牙语", NameEN: "Spanish"},
	{ID: "pt", NameZH: "葡萄牙语", NameEN: "Portuguese"},
	{ID: "fr", NameZH: "法语", NameEN: "French"},
	{ID: "de", NameZH: "德语", NameEN: "German"},
	{ID: "ja", NameZH: "日语", NameEN: "Japanese"},
	{ID: "id", NameZH: "印度尼西亚语", NameEN: "Indonesian"},
	{ID: "ru", NameZH: "俄语", NameEN: "Russian"},
	{ID: "it", NameZH: "意大利语", NameEN: "Italian"},
	{ID: "tr", NameZH: "土耳其语", NameEN: "Turkish"},
	{ID: "ar", NameZH: "阿拉伯语", NameEN: "Arabic"},
	{ID: "hi", NameZH: "印地语", NameEN: "Hindi"},
	{ID: "ko", NameZH: "韩语", NameEN: "Korean"},
	{ID: "nl", NameZH: "荷兰语", NameEN: "Dutch"},
	{ID: "pl", NameZH: "波兰语", NameEN: "Polish"},
	{ID: "vi", NameZH: "越南语", NameEN: "Vietnamese"},
	{ID: "uk", NameZH: "乌克兰语", NameEN: "Ukrainian"},
	{ID: "sv", NameZH: "瑞典语", NameEN: "Swedish"},
	{ID: "da", NameZH: "丹麦语", NameEN: "Danish"},
	{ID: "nb", NameZH: "书面挪威语", NameEN: "Norwegian Bokmål"},
	{ID: "no", NameZH: "挪威语", NameEN: "Norwegian"},
	{ID: "th", NameZH: "泰语", NameEN: "Thai"},
	{ID: "ro", NameZH: "罗马尼亚语", NameEN: "Romanian"},
	{ID: "ms", NameZH: "马来语", NameEN: "Malay"},
	{ID: "bn", NameZH: "孟加拉语", NameEN: "Bangla"},
	{ID: "mr", NameZH: "马拉地语", NameEN: "Marathi"},
	{ID: "ta", NameZH: "泰米尔语", NameEN: "Tamil"},
	{ID: "te", NameZH: "泰卢固语", NameEN: "Telugu"},
	{ID: "gu", NameZH: "古吉拉特语", NameEN: "Gujarati"},
	{ID: "ur", NameZH: "乌尔都语", NameEN: "Urdu"},
	{ID: "ml", NameZH: "马拉雅拉姆语", NameEN: "Malayalam"},
	{ID: "kn", NameZH: "卡纳达语", NameEN: "Kannada"},
	{ID: "sw", NameZH: "斯瓦希里语", NameEN: "Swahili"},
	{ID: "zh", NameZH: "普通话", NameEN: "Mandarin Chinese"},
	{ID: "af", NameZH: "南非荷兰语", NameEN: "Afrikaans"},
	{ID: "hy", NameZH: "亚美尼亚语", NameEN: "Armenian"},
	{ID: "az", NameZH: "阿塞拜疆语", NameEN: "Azerbaijani"},
	{ID: "be", NameZH: "白俄罗斯语", NameEN: "Belarusian"},
	{ID: "bs", NameZH: "波斯尼亚语", NameEN: "Bosnian"},
	{ID: "bg", NameZH: "保加利亚语", NameEN: "Bulgarian"},
	{ID: "ca", NameZH: "加泰罗尼亚语", NameEN: "Catalan"},
	{ID: "hr", NameZH: "克罗地亚语", NameEN: "Croatian"},
	{ID: "cs", NameZH: "捷克语", NameEN: "Czech"},
	{ID: "et", NameZH: "爱沙尼亚语", NameEN: "Estonian"},
	{ID: "fi", NameZH: "芬兰语", NameEN: "Finnish"},
	{ID: "gl", NameZH: "加利西亚语", NameEN: "Galician"},
	{ID: "ka", NameZH: "格鲁吉亚语", NameEN: "Georgian"},
	{ID: "el", NameZH: "希腊语", NameEN: "Greek"},
	{ID: "he", NameZH: "希伯来语", NameEN: "Hebrew"},
	{ID: "hu", NameZH: "匈牙利语", NameEN: "Hungarian"},
	{ID: "is", NameZH: "冰岛语", NameEN: "Icelandic"},
	{ID: "kk", NameZH: "哈萨克语", NameEN: "Kazakh"},
	{ID: "lv", NameZH: "拉脱维亚语", NameEN: "Latvian"},
	{ID: "lt", NameZH: "立陶宛语", NameEN: "Lithuanian"},
	{ID: "mk", NameZH: "马其顿语", NameEN: "Macedonian"},
	{ID: "mi", NameZH: "毛利语", NameEN: "Māori"},
	{ID: "ne", NameZH: "尼泊尔语", NameEN: "Nepali"},
	{ID: "fa", NameZH: "波斯语", NameEN: "Persian"},
	{ID: "sr", NameZH: "塞尔维亚语", NameEN: "Serbian"},
	{ID: "sk", NameZH: "斯洛伐克语", NameEN: "Slovak"},
	{ID: "sl", NameZH: "斯洛文尼亚语", NameEN: "Slovenian"},
	{ID: "tl", NameZH: "菲律宾语", NameEN: "Filipino"},
	{ID: "cy", NameZH: "威尔士语", NameEN: "Welsh"},
	{ID: "amh", NameZH: "阿姆哈拉语", NameEN: "Amharic"},
	{ID: "mya", NameZH: "缅甸语", NameEN: "Burmese"},
	{ID: "yue", NameZH: "粤语（繁体）", NameEN: "Cantonese (Traditional Chinese)"},
	{ID: "fil", NameZH: "菲律宾语", NameEN: "Filipino"},
	{ID: "gle", NameZH: "爱尔兰语", NameEN: "Irish"},
	{ID: "mon", NameZH: "蒙古语", NameEN: "Mongolian"},
	{ID: "som", NameZH: "索马里语", NameEN: "Somali"},
	{ID: "zh-cn", NameZH: "简体中文", NameEN: "Simplified Chinese"},
	{ID: "zh-tw", NameZH: "繁体中文（台湾）", NameEN: "Traditional Chinese (Taiwan)"},
	{ID: "zh-hk", NameZH: "繁体中文（香港）", NameEN: "Traditional Chinese (Hong Kong)"},
}

var allowedRealtimeVoices = choiceSetVoices(voiceChoices)
var allowedVoiceModes = choiceSet(voiceModeChoices)
var allowedLanguageCodes = languageSet(languageChoices)

var realtimeVoiceAliases = map[string]string{
	"arbor":  "fathom",
	"sol":    "glimmer",
	"spruce": "orbit",
}

// Config returns a copy of the non-secret capability document.
func Config() PublicConfig {
	return PublicConfig{
		Version: "v1",
		Defaults: SessionDefaults{
			Voice:        defaultVoice,
			VoiceMode:    defaultVoiceMode,
			LanguageCode: defaultLanguageCode,
		},
		Voices:     append([]VoiceChoice(nil), voiceChoices...),
		VoiceModes: append([]Choice(nil), voiceModeChoices...),
		Languages:  append([]LanguageChoice(nil), languageChoices...),
		WebRTC: WebRTCConfig{
			DataChannel: DataChannelConfig{Label: "oai-events", Negotiated: true, ID: 0},
			ICEServers: []ICEServerConfig{
				{URLs: []string{"stun:stun.l.google.com:19302"}},
				{URLs: []string{"stun:stun4.l.google.com:19302"}},
			},
			ReceiveAudio: true,
			ReceiveVideo: false,
		},
	}
}

func normalizeSessionOptions(voice, voiceMode, languageCode string) (SessionDefaults, error) {
	voice = strings.ToLower(strings.TrimSpace(voice))
	if voice == "" {
		voice = defaultVoice
	}
	if alias, ok := realtimeVoiceAliases[voice]; ok {
		voice = alias
	}
	if _, ok := allowedRealtimeVoices[voice]; !ok {
		return SessionDefaults{}, &ServiceError{Message: "unsupported voice", StatusCode: 400}
	}

	voiceMode = strings.ToLower(strings.TrimSpace(voiceMode))
	if voiceMode == "" {
		voiceMode = defaultVoiceMode
	}
	if _, ok := allowedVoiceModes[voiceMode]; !ok {
		return SessionDefaults{}, &ServiceError{Message: "unsupported voice_mode", StatusCode: 400}
	}

	languageCode = strings.ToLower(strings.TrimSpace(languageCode))
	if languageCode == "" {
		languageCode = defaultLanguageCode
	}
	if _, ok := allowedLanguageCodes[languageCode]; !ok {
		return SessionDefaults{}, &ServiceError{Message: "unsupported language_code", StatusCode: 400}
	}

	return SessionDefaults{Voice: voice, VoiceMode: voiceMode, LanguageCode: languageCode}, nil
}

func choiceSet(items []Choice) map[string]struct{} {
	result := make(map[string]struct{}, len(items))
	for _, item := range items {
		result[item.ID] = struct{}{}
	}
	return result
}

func choiceSetVoices(items []VoiceChoice) map[string]struct{} {
	result := make(map[string]struct{}, len(items))
	for _, item := range items {
		result[item.ID] = struct{}{}
	}
	return result
}

func languageSet(items []LanguageChoice) map[string]struct{} {
	result := make(map[string]struct{}, len(items))
	for _, item := range items {
		result[item.ID] = struct{}{}
	}
	return result
}
