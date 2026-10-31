package apicall

import (
	"encoding/json"
	"fmt"
)

type Request struct {
	AuthIndex string            `json:"authIndex"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Header    map[string]string `json:"header,omitempty"`
	Data      any               `json:"data,omitempty"`
}

// MarshalJSON 将调用数据封装为 CPA 要求的字符串；已是字符串时不再编码，避免双重转义。
func (r Request) MarshalJSON() ([]byte, error) {
	type alias Request
	encoded := alias(r)
	if r.Data != nil {
		if _, ok := r.Data.(string); !ok {
			dataBytes, err := json.Marshal(r.Data)
			if err != nil {
				return nil, err
			}
			encoded.Data = string(dataBytes)
		}
	}
	return json.Marshal(encoded)
}

// Response 同时保留 api-call 的 JSON 字符串 body 和解码后的文本。
// Body 供仍按原始 JSON 读取的调用方使用，BodyText 供额度解析使用，不将响应正文重编码为对象。
type Response struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header"`
	BodyText   string              `json:"-"`
	Body       json.RawMessage     `json:"body"`
}

func (r *Response) UnmarshalJSON(data []byte) error {
	var decoded struct {
		StatusCode int                 `json:"status_code"`
		Header     map[string][]string `json:"header"`
		Body       json.RawMessage     `json:"body"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	// 上游正文按字符串合同读取，保留空正文和非 JSON 文本；对象正文视为合同错误。
	var bodyText string
	if err := json.Unmarshal(decoded.Body, &bodyText); err != nil {
		return fmt.Errorf("decode api-call body: %w", err)
	}
	*r = Response{StatusCode: decoded.StatusCode, Header: decoded.Header, BodyText: bodyText, Body: decoded.Body}
	return nil
}
