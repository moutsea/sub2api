#!/usr/bin/env bash
# OpenAI 账号 Anthropic 协议兼容测试脚本
# 目标：验证同一个 API Key 是否可通过 Anthropic 接口（/v1/messages）正常访问

set -u

BASE_URL="${BASE_URL:-https://cfjwlpro.com}"
API_KEY="${API_KEY:-sk-35bfc3e2957a642251d2c0e31f4eacf1c4f3d80df3a6bde6f53183897a8fdcd0}"
ANTHROPIC_VERSION="${ANTHROPIC_VERSION:-2023-06-01}"
MODEL="${MODEL:-claude-sonnet-4-20250514}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass=0
fail=0

print_result() {
  local name="$1" status="$2" detail="${3:-}"
  if [ "$status" = "ok" ]; then
    echo -e "${GREEN}✓${NC} $name"
    pass=$((pass + 1))
  else
    echo -e "${RED}✗${NC} $name${detail:+ — $detail}"
    fail=$((fail + 1))
  fi
}

run_messages() {
  local payload="$1"
  curl -sS --max-time 45 -w "\nHTTP_STATUS:%{http_code}" \
    "$BASE_URL/v1/messages" \
    -H "Content-Type: application/json" \
    -H "x-api-key: $API_KEY" \
    -H "anthropic-version: $ANTHROPIC_VERSION" \
    -d "$payload" 2>&1
}

run_models() {
  curl -sS --max-time 20 -w "\nHTTP_STATUS:%{http_code}" \
    "$BASE_URL/v1/models" \
    -H "x-api-key: $API_KEY" \
    -H "anthropic-version: $ANTHROPIC_VERSION" 2>&1
}

split_status_body() {
  local raw="$1"
  HTTP_STATUS="$(printf '%s' "$raw" | sed -n 's/.*HTTP_STATUS:\([0-9][0-9][0-9]\).*/\1/p' | tail -1)"
  HTTP_BODY="$(printf '%s' "$raw" | sed '/HTTP_STATUS:[0-9][0-9][0-9]/d')"
}

echo "========================================="
echo " OpenAI账号 -> Anthropic接口 兼容性测试"
echo " BASE_URL: $BASE_URL"
echo " MODEL:    $MODEL"
echo " KEY:      ${API_KEY:0:10}..."
echo "========================================="
echo ""

# ---- Test 1: /v1/messages 基础非流式 ----
echo "--- Test 1: /v1/messages 基础非流式 ---"
raw=$(run_messages "{\"model\":\"$MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"Reply with only OK.\"}]}")
split_status_body "$raw"

if [ "$HTTP_STATUS" = "200" ]; then
  print_result "messages 非流式状态码 200" "ok"
else
  print_result "messages 非流式状态码 200" "fail" "status=$HTTP_STATUS body=$(echo "$HTTP_BODY" | head -1)"
fi

if echo "$HTTP_BODY" | grep -q '"type":"message"'; then
  print_result "messages 返回 Claude message 格式" "ok"
else
  print_result "messages 返回 Claude message 格式" "fail" "body=$(echo "$HTTP_BODY" | head -1)"
fi

if echo "$HTTP_BODY" | grep -q '"role":"assistant"'; then
  print_result "messages 返回 assistant 角色" "ok"
else
  print_result "messages 返回 assistant 角色" "fail" "missing role"
fi

echo ""

# ---- Test 2: /v1/messages 流式 ----
echo "--- Test 2: /v1/messages 流式 ---"
raw=$(run_messages "{\"model\":\"$MODEL\",\"stream\":true,\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"Say hi in 2 words.\"}]}")
split_status_body "$raw"

if [ "$HTTP_STATUS" = "200" ]; then
  print_result "messages 流式状态码 200" "ok"
else
  print_result "messages 流式状态码 200" "fail" "status=$HTTP_STATUS body=$(echo "$HTTP_BODY" | head -1)"
fi

if echo "$HTTP_BODY" | grep -q 'event: message_start'; then
  print_result "流式包含 message_start" "ok"
else
  print_result "流式包含 message_start" "fail" "missing message_start"
fi

if echo "$HTTP_BODY" | grep -q 'event: message_stop'; then
  print_result "流式包含 message_stop" "ok"
else
  print_result "流式包含 message_stop" "fail" "missing message_stop"
fi

echo ""

# ---- Test 3: tool use ----
echo "--- Test 3: tool use ---"
raw=$(run_messages "{\"model\":\"$MODEL\",\"max_tokens\":128,\"messages\":[{\"role\":\"user\",\"content\":\"What is weather in Tokyo?\"}],\"tools\":[{\"name\":\"get_weather\",\"description\":\"Get weather\",\"input_schema\":{\"type\":\"object\",\"properties\":{\"city\":{\"type\":\"string\"}},\"required\":[\"city\"]}}],\"tool_choice\":{\"type\":\"auto\"}}")
split_status_body "$raw"

if [ "$HTTP_STATUS" = "200" ]; then
  print_result "tool use 请求可达" "ok"
else
  print_result "tool use 请求可达" "fail" "status=$HTTP_STATUS body=$(echo "$HTTP_BODY" | head -1)"
fi

if echo "$HTTP_BODY" | grep -q 'tool_use\|"type":"message"'; then
  print_result "tool use 响应格式可解析" "ok"
else
  print_result "tool use 响应格式可解析" "fail" "body=$(echo "$HTTP_BODY" | head -1)"
fi

echo ""

# ---- Test 4: thinking adaptive ----
echo "--- Test 4: thinking adaptive ---"
raw=$(run_messages "{\"model\":\"$MODEL\",\"max_tokens\":96,\"thinking\":{\"type\":\"adaptive\",\"budget_tokens\":4000},\"messages\":[{\"role\":\"user\",\"content\":\"Answer briefly: 1+1=?\"}]}")
split_status_body "$raw"

if [ "$HTTP_STATUS" = "200" ]; then
  print_result "adaptive thinking 请求成功" "ok"
else
  print_result "adaptive thinking 请求成功" "fail" "status=$HTTP_STATUS body=$(echo "$HTTP_BODY" | head -1)"
fi

if echo "$HTTP_BODY" | grep -q '"type":"message"\|event: message_start'; then
  print_result "adaptive thinking 响应格式正常" "ok"
else
  print_result "adaptive thinking 响应格式正常" "fail" "body=$(echo "$HTTP_BODY" | head -1)"
fi

echo ""

# ---- Test 5: /v1/models ----
echo "--- Test 5: /v1/models ---"
raw=$(run_models)
split_status_body "$raw"

if [ "$HTTP_STATUS" = "200" ]; then
  print_result "/v1/models 状态码 200" "ok"
else
  print_result "/v1/models 状态码 200" "fail" "status=$HTTP_STATUS body=$(echo "$HTTP_BODY" | head -1)"
fi

if echo "$HTTP_BODY" | grep -qi '"data"\|"id"'; then
  print_result "/v1/models 返回模型列表" "ok"
else
  print_result "/v1/models 返回模型列表" "fail" "body=$(echo "$HTTP_BODY" | head -1)"
fi

echo ""
echo "========================================="
echo -e " 结果: ${GREEN}${pass} 通过${NC}, ${RED}${fail} 失败${NC}"
echo "========================================="

exit "$fail"
