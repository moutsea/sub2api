#!/bin/bash
# Kiro OpenAI Compat 测试脚本
# 测试 /v1/chat/completions 端点通过 Kiro 账号调用 Claude

BASE_URL="https://cfjwlpro.com"
API_KEY="sk-3ea98b833398ce2468be603d568208fc88988c5dfa4104a8374715076fa4342e"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass=0
fail=0

print_result() {
    local name="$1" status="$2" detail="$3"
    if [ "$status" = "ok" ]; then
        echo -e "${GREEN}✓${NC} $name"
        ((pass++))
    else
        echo -e "${RED}✗${NC} $name — $detail"
        ((fail++))
    fi
}

echo "========================================="
echo " Kiro OpenAI Compat 测试"
echo " $BASE_URL"
echo "========================================="
echo ""

# ---- Test 1: 基础文本对话 (streaming) ----
echo "--- Test 1: 基础文本对话 (streaming) ---"
resp=$(curl -sS --max-time 30 "$BASE_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "stream": true,
    "max_tokens": 100,
    "messages": [
      {"role": "user", "content": "Say hello in exactly 3 words."}
    ]
  }' 2>&1)

if echo "$resp" | grep -q "chat.completion.chunk"; then
    print_result "streaming 响应格式正确" "ok"
else
    print_result "streaming 响应格式正确" "fail" "$(echo "$resp" | head -3)"
fi

if echo "$resp" | grep -q '"role":"assistant"'; then
    print_result "streaming 包含 role:assistant" "ok"
else
    print_result "streaming 包含 role:assistant" "fail" "missing role"
fi

if echo "$resp" | grep -q '\[DONE\]'; then
    print_result "streaming 以 [DONE] 结束" "ok"
else
    print_result "streaming 以 [DONE] 结束" "fail" "missing [DONE]"
fi

if echo "$resp" | grep -q '"finish_reason"'; then
    print_result "streaming 包含 finish_reason" "ok"
else
    print_result "streaming 包含 finish_reason" "fail" "missing finish_reason"
fi

echo ""

# ---- Test 2: 非 streaming 对话 ----
echo "--- Test 2: 非 streaming 对话 ---"
resp=$(curl -sS --max-time 30 "$BASE_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "stream": false,
    "max_tokens": 100,
    "messages": [
      {"role": "user", "content": "Reply with only the word OK."}
    ]
  }' 2>&1)

if echo "$resp" | grep -q '"chat.completion"'; then
    print_result "non-stream 响应格式正确" "ok"
else
    print_result "non-stream 响应格式正确" "fail" "$(echo "$resp" | head -1)"
fi

if echo "$resp" | grep -q '"finish_reason"'; then
    print_result "non-stream 包含 finish_reason" "ok"
else
    print_result "non-stream 包含 finish_reason" "fail" "missing"
fi

if echo "$resp" | grep -q '"usage"'; then
    print_result "non-stream 包含 usage" "ok"
else
    print_result "non-stream 包含 usage" "fail" "missing"
fi

echo ""

# ---- Test 3: System message ----
echo "--- Test 3: System message ---"
resp=$(curl -sS --max-time 30 "$BASE_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "stream": true,
    "max_tokens": 50,
    "messages": [
      {"role": "system", "content": "You must reply in exactly one word: PONG"},
      {"role": "user", "content": "PING"}
    ]
  }' 2>&1)

if echo "$resp" | grep -q "chat.completion.chunk"; then
    print_result "system message 正常传递" "ok"
else
    print_result "system message 正常传递" "fail" "$(echo "$resp" | head -3)"
fi

echo ""

# ---- Test 4: Tool use ----
echo "--- Test 4: Tool use (function calling) ---"
resp=$(curl -sS --max-time 30 "$BASE_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "stream": true,
    "max_tokens": 200,
    "messages": [
      {"role": "user", "content": "What is the weather in Tokyo?"}
    ],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get current weather for a city",
        "parameters": {
          "type": "object",
          "properties": {
            "city": {"type": "string", "description": "City name"}
          },
          "required": ["city"]
        }
      }
    }],
    "tool_choice": "auto"
  }' 2>&1)

if echo "$resp" | grep -q "tool_calls"; then
    print_result "tool_calls 出现在响应中" "ok"
else
    print_result "tool_calls 出现在响应中" "fail" "$(echo "$resp" | head -5)"
fi

if echo "$resp" | grep -q "get_weather"; then
    print_result "tool name 正确" "ok"
else
    print_result "tool name 正确" "fail" "missing get_weather"
fi

echo ""

# ---- Test 5: 多轮对话 ----
echo "--- Test 5: 多轮对话 ---"
resp=$(curl -sS --max-time 30 "$BASE_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "stream": true,
    "max_tokens": 50,
    "messages": [
      {"role": "user", "content": "My name is Alice."},
      {"role": "assistant", "content": "Nice to meet you, Alice!"},
      {"role": "user", "content": "What is my name?"}
    ]
  }' 2>&1)

if echo "$resp" | grep -qi "Alice"; then
    print_result "多轮对话上下文保持" "ok"
else
    print_result "多轮对话上下文保持" "fail" "response may not contain Alice"
fi

echo ""

# ---- Test 6: 错误处理 - 空 messages ----
echo "--- Test 6: 错误处理 ---"
resp=$(curl -sS --max-time 10 "$BASE_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "messages": []
  }' 2>&1)

if echo "$resp" | grep -qi "error"; then
    print_result "空 messages 返回错误" "ok"
else
    print_result "空 messages 返回错误" "fail" "$(echo "$resp" | head -1)"
fi

echo ""

# ---- Test 7: /v1/models 端点 ----
echo "--- Test 7: /v1/models ---"
resp=$(curl -sS --max-time 10 "$BASE_URL/v1/models" \
  -H "Authorization: Bearer $API_KEY" 2>&1)

if echo "$resp" | grep -qi "claude\|model"; then
    print_result "/v1/models 返回模型列表" "ok"
else
    print_result "/v1/models 返回模型列表" "fail" "$(echo "$resp" | head -1)"
fi

echo ""
echo "========================================="
echo -e " 结果: ${GREEN}$pass 通过${NC}, ${RED}$fail 失败${NC}"
echo "========================================="

exit $fail
