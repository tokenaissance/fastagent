#!/bin/bash
# FastClaw → FastAgent 精确替换脚本
# 基于 review 结果，只修改需要修改的内容

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${RED}⚠️  警告：此脚本将修改大量文件！${NC}"
echo -e "${YELLOW}建议：${NC}"
echo "  1. 确保当前在 git 分支中"
echo "  2. 已经提交了所有未保存的更改"
echo "  3. 可以随时使用 git checkout . 回滚"
echo ""
read -p "确认继续？(yes/no): " confirm

if [ "$confirm" != "yes" ]; then
    echo "已取消"
    exit 0
fi

echo ""
echo -e "${GREEN}=== 开始执行替换 ===${NC}\n"

# 统计
total_files=0

# 替换函数
replace_in_files() {
    local pattern=$1
    local replacement=$2
    local description=$3

    echo -e "${BLUE}处理: $description${NC}"

    # 查找文件（排除前端、依赖目录）
    local files=$(grep -rl "$pattern" \
        --include="*.go" \
        --include="*.sh" \
        --include="*.yaml" \
        --include="*.yml" \
        --include="*.md" \
        --include="Dockerfile" \
        --include="Makefile" \
        --exclude-dir=".git" \
        --exclude-dir="node_modules" \
        --exclude-dir="dist" \
        --exclude-dir="vendor" \
        --exclude-dir="web" \
        . 2>/dev/null || true)

    if [ -n "$files" ]; then
        local count=0
        for file in $files; do
            if [ -f "$file" ]; then
                if [[ "$OSTYPE" == "darwin"* ]]; then
                    sed -i '' "s|$pattern|$replacement|g" "$file"
                else
                    sed -i "s|$pattern|$replacement|g" "$file"
                fi
                count=$((count + 1))
            fi
        done
        echo "  ✓ 修改了 $count 个文件"
        total_files=$((total_files + count))
    else
        echo "  - 未找到匹配文件"
    fi
}

# ============================================================
# 1. 环境变量名称（35+ 个）
# ============================================================
echo -e "\n${YELLOW}1. 替换环境变量名称${NC}"
echo "----------------------------------------"

env_vars=(
    "FASTAGENT_HOME:FASTAGENT_HOME"
    "FASTAGENT_PORT:FASTAGENT_PORT"
    "FASTAGENT_BIND:FASTAGENT_BIND"
    "FASTAGENT_STORAGE_TYPE:FASTAGENT_STORAGE_TYPE"
    "FASTAGENT_STORAGE_DSN:FASTAGENT_STORAGE_DSN"
    "FASTAGENT_STORAGE_AUTO_MIGRATE:FASTAGENT_STORAGE_AUTO_MIGRATE"
    "FASTAGENT_SANDBOX_ENABLED:FASTAGENT_SANDBOX_ENABLED"
    "FASTAGENT_SANDBOX_BACKEND:FASTAGENT_SANDBOX_BACKEND"
    "FASTAGENT_SANDBOX_IMAGE:FASTAGENT_SANDBOX_IMAGE"
    "FASTAGENT_SANDBOX_BOXLITE_URL:FASTAGENT_SANDBOX_BOXLITE_URL"
    "FASTAGENT_SANDBOX_BOXLITE_CLIENT_ID:FASTAGENT_SANDBOX_BOXLITE_CLIENT_ID"
    "FASTAGENT_SANDBOX_BOXLITE_PREFIX:FASTAGENT_SANDBOX_BOXLITE_PREFIX"
    "FASTAGENT_OBJECT_STORE_TYPE:FASTAGENT_OBJECT_STORE_TYPE"
    "FASTAGENT_OBJECT_STORE_LOCAL_ROOT:FASTAGENT_OBJECT_STORE_LOCAL_ROOT"
    "FASTAGENT_OBJECT_STORE_REGION:FASTAGENT_OBJECT_STORE_REGION"
    "FASTAGENT_OBJECT_STORE_BUCKET:FASTAGENT_OBJECT_STORE_BUCKET"
    "FASTAGENT_OBJECT_STORE_PREFIX:FASTAGENT_OBJECT_STORE_PREFIX"
    "FASTAGENT_OBJECT_STORE_ACCESSKEY:FASTAGENT_OBJECT_STORE_ACCESSKEY"
    "FASTAGENT_OBJECT_STORE_SECRETKEY:FASTAGENT_OBJECT_STORE_SECRETKEY"
    "FASTAGENT_OBJECT_STORE_ACCOUNTID:FASTAGENT_OBJECT_STORE_ACCOUNTID"
    "FASTAGENT_OBJECT_STORE_ENDPOINT:FASTAGENT_OBJECT_STORE_ENDPOINT"
    "FASTAGENT_OBJECT_STORE_USESSL:FASTAGENT_OBJECT_STORE_USESSL"
    "FASTAGENT_OBJECT_STORE_ALIYUN_INTERNAL:FASTAGENT_OBJECT_STORE_ALIYUN_INTERNAL"
    "FASTAGENT_LOG_LEVEL:FASTAGENT_LOG_LEVEL"
    "FASTAGENT_DEPLOY:FASTAGENT_DEPLOY"
    "FASTAGENT_ALLOW_HOST_EXEC:FASTAGENT_ALLOW_HOST_EXEC"
    "FASTAGENT_INSTALL_DIR:FASTAGENT_INSTALL_DIR"
    "FASTAGENT_MODE:FASTAGENT_MODE"
    "FASTAGENT_AUTH_TOKEN:FASTAGENT_AUTH_TOKEN"
    "FASTAGENT_SEARXNG_ENDPOINT:FASTAGENT_SEARXNG_ENDPOINT"
    "FASTAGENT_PLUGIN_CHAT_SEND_DELAY_MS:FASTAGENT_PLUGIN_CHAT_SEND_DELAY_MS"
    "FASTAGENT_DUMP_LLM:FASTAGENT_DUMP_LLM"
    "FASTAGENT_DUMP_LLM_FILE:FASTAGENT_DUMP_LLM_FILE"
    "FASTAGENT_AGENT_ID:FASTAGENT_AGENT_ID"
    "FASTAGENT_DAEMON_IDLE_TIMEOUT_SECONDS:FASTAGENT_DAEMON_IDLE_TIMEOUT_SECONDS"
)

for var_pair in "${env_vars[@]}"; do
    old_var="${var_pair%%:*}"
    new_var="${var_pair##*:}"
    replace_in_files "$old_var" "$new_var" "$old_var -> $new_var"
done

# ============================================================
# 2. 文件系统路径
# ============================================================
echo -e "\n${YELLOW}2. 替换文件系统路径${NC}"
echo "----------------------------------------"

replace_in_files '\.fastagent' ".fastagent" "~/.fastagent -> ~/.fastagent"
replace_in_files '/\.fastagent' "/.fastagent" "/data/.fastagent -> /data/.fastagent"

# ============================================================
# 3. HTTP Headers
# ============================================================
echo -e "\n${YELLOW}3. 替换 HTTP Headers${NC}"
echo "----------------------------------------"

replace_in_files "x-fastagent-agent-id" "x-fastagent-agent-id" "x-fastagent-agent-id -> x-fastagent-agent-id"
replace_in_files "x-fastagent-session-key" "x-fastagent-session-key" "x-fastagent-session-key -> x-fastagent-session-key"
replace_in_files "x-fastagent-channel" "x-fastagent-channel" "x-fastagent-channel -> x-fastagent-channel"

# ============================================================
# 4. Cookie 名称
# ============================================================
echo -e "\n${YELLOW}4. 替换 Cookie 名称${NC}"
echo "----------------------------------------"

replace_in_files "fastagent_session" "fastagent_session" "fastagent_session -> fastagent_session"
replace_in_files "fastagent-affinity" "fastagent-affinity" "fastagent-affinity -> fastagent-affinity"

# ============================================================
# 5. 命令和品牌名
# ============================================================
echo -e "\n${YELLOW}5. 替换命令和品牌名${NC}"
echo "----------------------------------------"

replace_in_files "fastagent upgrade" "fastagent upgrade" "fastagent upgrade -> fastagent upgrade"
replace_in_files "fastagent version" "fastagent version" "fastagent version -> fastagent version"
replace_in_files "FastAgent:" "FastAgent:" "FastAgent: -> FastAgent:"
replace_in_files "fastagent connect dialog" "fastagent connect dialog" "fastagent connect dialog -> fastagent connect dialog"

# ============================================================
# 6. Bot 用户名示例（注释中）
# ============================================================
echo -e "\n${YELLOW}6. 替换注释中的示例${NC}"
echo "----------------------------------------"

replace_in_files "mike_fastagent_bot" "mike_fastagent_bot" "mike_fastagent_bot -> mike_fastagent_bot"

# ============================================================
# 7. PostgreSQL 数据库名称
# ============================================================
echo -e "\n${YELLOW}7. 替换 PostgreSQL 数据库名称${NC}"
echo "----------------------------------------"

replace_in_files "POSTGRES_DB: fastagent" "POSTGRES_DB: fastagent" "POSTGRES_DB: fastagent -> fastagent"
replace_in_files "POSTGRES_USER: fastagent" "POSTGRES_USER: fastagent" "POSTGRES_USER: fastagent -> fastagent"
replace_in_files "postgres://fastagent:" "postgres://fastagent:" "postgres://fastagent: -> postgres://fastagent:"
replace_in_files "@host:5432/fastagent" "@host:5432/fastagent" "@.../fastclaw -> @.../fastagent"
replace_in_files 'pg_isready", "-U", "fastagent"' 'pg_isready", "-U", "fastagent"' "pg_isready -U fastclaw -> fastagent"

# ============================================================
# 8. Helm Chart 名称和标签
# ============================================================
echo -e "\n${YELLOW}8. 替换 Helm Chart 相关${NC}"
echo "----------------------------------------"

# Chart.yaml
if [ -f "deploy/helm/fastclaw/Chart.yaml" ]; then
    if [[ "$OSTYPE" == "darwin"* ]]; then
        sed -i '' 's/^name: fastclaw$/name: fastagent/' deploy/helm/fastclaw/Chart.yaml
        sed -i '' 's/FastClaw — Cloud AI Agent Runtime/FastAgent — Cloud AI Agent Runtime/' deploy/helm/fastclaw/Chart.yaml
    else
        sed -i 's/^name: fastclaw$/name: fastagent/' deploy/helm/fastclaw/Chart.yaml
        sed -i 's/FastClaw — Cloud AI Agent Runtime/FastAgent — Cloud AI Agent Runtime/' deploy/helm/fastclaw/Chart.yaml
    fi
    echo "  ✓ 修改了 Chart.yaml"
fi

# _helpers.tpl - 模板函数名
if [ -f "deploy/helm/fastclaw/templates/_helpers.tpl" ]; then
    if [[ "$OSTYPE" == "darwin"* ]]; then
        sed -i '' 's/fastclaw\.fullname/fastagent.fullname/g' deploy/helm/fastclaw/templates/_helpers.tpl
        sed -i '' 's/fastclaw\.labels/fastagent.labels/g' deploy/helm/fastclaw/templates/_helpers.tpl
        sed -i '' 's/fastclaw\.dsn/fastagent.dsn/g' deploy/helm/fastclaw/templates/_helpers.tpl
        sed -i '' 's/app\.kubernetes\.io\/name: fastclaw/app.kubernetes.io\/name: fastagent/' deploy/helm/fastclaw/templates/_helpers.tpl
    else
        sed -i 's/fastclaw\.fullname/fastagent.fullname/g' deploy/helm/fastclaw/templates/_helpers.tpl
        sed -i 's/fastclaw\.labels/fastagent.labels/g' deploy/helm/fastclaw/templates/_helpers.tpl
        sed -i 's/fastclaw\.dsn/fastagent.dsn/g' deploy/helm/fastclaw/templates/_helpers.tpl
        sed -i 's/app\.kubernetes\.io\/name: fastclaw/app.kubernetes.io\/name: fastagent/' deploy/helm/fastclaw/templates/_helpers.tpl
    fi
    echo "  ✓ 修改了 _helpers.tpl"
fi

# 所有模板文件中的函数引用
for file in deploy/helm/fastclaw/templates/*.yaml; do
    if [ -f "$file" ]; then
        if [[ "$OSTYPE" == "darwin"* ]]; then
            sed -i '' 's/"fastclaw\.fullname"/"fastagent.fullname"/g' "$file"
            sed -i '' 's/"fastclaw\.labels"/"fastagent.labels"/g' "$file"
            sed -i '' 's/"fastclaw\.dsn"/"fastagent.dsn"/g' "$file"
        else
            sed -i 's/"fastclaw\.fullname"/"fastagent.fullname"/g' "$file"
            sed -i 's/"fastclaw\.labels"/"fastagent.labels"/g' "$file"
            sed -i 's/"fastclaw\.dsn"/"fastagent.dsn"/g' "$file"
        fi
    fi
done
echo "  ✓ 修改了所有模板文件中的函数引用"

# 重命名 Helm Chart 目录
if [ -d "deploy/helm/fastclaw" ]; then
    mv deploy/helm/fastclaw deploy/helm/fastagent
    echo "  ✓ 重命名目录: deploy/helm/fastclaw -> deploy/helm/fastagent"
fi

# ============================================================
# 9. 文档中的剩余 fastclaw 引用（排除不应修改的）
# ============================================================
echo -e "\n${YELLOW}9. 替换文档和注释中的其他 fastclaw 引用${NC}"
echo "----------------------------------------"

# 只在文档和注释中替换剩余的 fastclaw
# 排除：Go module 路径、Docker 镜像名
echo "  处理 Markdown 文档..."
for file in $(find . -name "*.md" -not -path "./web/*" -not -path "./.git/*" -not -path "./node_modules/*" 2>/dev/null); do
    if [ -f "$file" ]; then
        # 排除 Go module 路径和 Docker 镜像名
        if [[ "$OSTYPE" == "darwin"* ]]; then
            # 不替换 github.com/fastclaw-ai/fastclaw
            # 不替换 ghcr.io/fastclaw-ai/fastclaw
            # 不替换 thinkany/fastclaw-sandbox
            sed -i '' \
                -e '/github\.com\/fastclaw-ai\/fastclaw/!s/fastclaw/fastagent/g' \
                -e '/ghcr\.io\/fastclaw-ai\/fastclaw/!s/fastclaw/fastagent/g' \
                -e '/thinkany\/fastclaw-sandbox/!s/fastclaw/fastagent/g' \
                "$file" 2>/dev/null || true
        else
            sed -i \
                -e '/github\.com\/fastclaw-ai\/fastclaw/!s/fastclaw/fastagent/g' \
                -e '/ghcr\.io\/fastclaw-ai\/fastclaw/!s/fastclaw/fastagent/g' \
                -e '/thinkany\/fastclaw-sandbox/!s/fastclaw/fastagent/g' \
                "$file" 2>/dev/null || true
        fi
    fi
done
echo "  ✓ 处理了 Markdown 文档"

# ============================================================
# 总结
# ============================================================
echo -e "\n${GREEN}=== 替换完成 ===${NC}\n"
echo "统计信息："
echo "  修改的文件总数: $total_files+"
echo ""
echo -e "${YELLOW}后续步骤：${NC}"
echo "  1. 检查 git diff 确认修改正确"
echo "  2. 特别检查 go.mod 和 Docker 镜像名未被修改"
echo "  3. 运行测试确保功能正常"
echo "  4. 更新部署环境的环境变量"
echo ""
echo -e "${RED}重要提醒：${NC}"
echo "  - Helm Chart 目录已重命名为 deploy/helm/fastagent"
echo "  - K8s 环境需要重新安装 Helm release"
echo "  - 用户需要重新登录（cookie 名称变更）"
echo "  - API 用户需要更新 HTTP header 名称"
echo ""
echo -e "${BLUE}已排除（未修改）：${NC}"
echo "  - web/src/ 前端源码"
echo "  - Go module 路径 (github.com/fastclaw-ai/fastclaw)"
echo "  - Docker 镜像名 (ghcr.io/fastclaw-ai/fastclaw, thinkany/fastclaw-sandbox)"
echo ""
echo -e "${YELLOW}验证命令：${NC}"
echo "  # 检查 Go module 路径未被修改"
echo "  grep 'github.com/fastclaw-ai/fastclaw' go.mod"
echo ""
echo "  # 检查 Docker 镜像名未被修改"
echo "  grep -r 'ghcr.io/fastclaw-ai/fastclaw' deploy/"
echo "  grep -r 'thinkany/fastclaw-sandbox' deploy/"
