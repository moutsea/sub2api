package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	openAIEnvironmentLocation = time.FixedZone("Asia/Singapore", 8*60*60)
	openAIEnvironmentPattern  = regexp.MustCompile(`(?s)<environment_context>.*?</environment_context>`)
	openAITimezonePattern     = regexp.MustCompile(`(?s)<timezone>.*?</timezone>`)
	openAICurrentDatePattern  = regexp.MustCompile(`(?s)<current_date>.*?</current_date>`)
)

func normalizeOpenAIEnvironmentText(text, currentDate string) string {
	timezoneTag := "<timezone>Asia/Singapore</timezone>"
	dateTag := "<current_date>" + currentDate + "</current_date>"
	if !openAIEnvironmentPattern.MatchString(text) {
		block := "<environment_context>\n" + timezoneTag + "\n" + dateTag + "\n</environment_context>"
		if strings.TrimSpace(text) == "" {
			return block
		}
		return text + "\n\n" + block
	}
	return openAIEnvironmentPattern.ReplaceAllStringFunc(text, func(block string) string {
		for _, field := range []struct {
			pattern *regexp.Regexp
			value   string
		}{
			{openAITimezonePattern, timezoneTag},
			{openAICurrentDatePattern, dateTag},
		} {
			if field.pattern.MatchString(block) {
				block = field.pattern.ReplaceAllString(block, field.value)
			} else {
				block = strings.TrimSuffix(block, "</environment_context>") + "\n" + field.value + "\n</environment_context>"
			}
		}
		return block
	})
}

func prepareOpenAIEnvironmentContext(account *Account, body []byte, messageField string) ([]byte, error) {
	if !account.IsOpenAI() {
		return body, nil
	}
	return applyOpenAIEnvironmentContext(body, messageField, time.Now())
}

func ValidateOpenAIEnvironmentContext(body []byte, messageField string) error {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return fmt.Errorf("OpenAI request must be a JSON object")
	}
	instructions := gjson.GetBytes(body, "instructions")
	if messageField == "input" && instructions.Exists() && instructions.Type != gjson.String && instructions.Type != gjson.Null {
		return fmt.Errorf("OpenAI instructions must be a string")
	}
	if messageField == "messages" && !gjson.GetBytes(body, "messages").IsArray() {
		return fmt.Errorf("OpenAI messages must be an array")
	}
	return nil
}

func applyOpenAIEnvironmentContext(body []byte, messageField string, now time.Time) ([]byte, error) {
	if err := ValidateOpenAIEnvironmentContext(body, messageField); err != nil {
		return nil, err
	}
	instructions := gjson.GetBytes(body, "instructions")
	currentDate := now.In(openAIEnvironmentLocation).Format("2006-01-02")
	paths := []string{"instructions"}
	for messageIndex, message := range gjson.GetBytes(body, messageField).Array() {
		role := message.Get("role").String()
		if role != "system" && role != "developer" {
			continue
		}
		path := messageField + "." + strconv.Itoa(messageIndex) + ".content"
		content := message.Get("content")
		if content.Type == gjson.String {
			paths = append(paths, path)
		} else if content.IsArray() {
			for partIndex, part := range content.Array() {
				if kind := part.Get("type").String(); kind == "text" || kind == "input_text" {
					paths = append(paths, path+"."+strconv.Itoa(partIndex)+".text")
				}
			}
		}
	}

	found := false
	for _, path := range paths {
		value := gjson.GetBytes(body, path)
		if value.Type != gjson.String || !openAIEnvironmentPattern.MatchString(value.String()) {
			continue
		}
		found = true
		updated := normalizeOpenAIEnvironmentText(value.String(), currentDate)
		if updated != value.String() {
			var err error
			body, err = sjson.SetBytes(body, path, updated)
			if err != nil {
				return nil, err
			}
		}
	}
	if found {
		return body, nil
	}
	if messageField == "messages" {
		messages := gjson.GetBytes(body, "messages")
		contextMessage, err := sjson.SetBytes([]byte(`{"role":"system"}`), "content", normalizeOpenAIEnvironmentText("", currentDate))
		if err != nil {
			return nil, err
		}
		contents := strings.TrimSpace(messages.Raw[1 : len(messages.Raw)-1])
		updated := "[" + string(contextMessage)
		if contents != "" {
			updated += "," + contents
		}
		return sjson.SetRawBytes(body, "messages", []byte(updated+"]"))
	}
	return sjson.SetBytes(body, "instructions", normalizeOpenAIEnvironmentText(instructions.String(), currentDate))
}
