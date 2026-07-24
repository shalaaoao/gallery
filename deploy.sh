#!/bin/bash

REMOTE="root@192.168.100.15"
REMOTE_DIR="/opt/data/gallery"
SERVICE="gogallery-server"
SUPERVISORCTL="/usr/bin/supervisorctl"

echo "🔨 编译 Linux 版..."
GOOS=linux GOARCH=amd64 go build -o gallery || { echo "❌ 编译失败"; exit 1; }
echo "✅ 编译完成"

echo "📦 上传到服务器..."
# SSH 服务器可能有限流，加重试
for i in 1 2 3; do
    scp -o ConnectTimeout=10 gallery "$REMOTE:$REMOTE_DIR/gallery.new" 2>&1 && break
    echo "⚠️  上传重试 $i..."
    sleep 2
done || { echo "❌ 上传失败"; exit 1; }
echo "✅ 上传完成"

echo "🔄 停服 → 替换 → 启动 ..."
for i in 1 2 3; do
    ssh -o ConnectTimeout=10 "$REMOTE" "
        $SUPERVISORCTL stop $SERVICE && \
        mv $REMOTE_DIR/gallery.new $REMOTE_DIR/gallery && \
        chmod +x $REMOTE_DIR/gallery && \
        $SUPERVISORCTL start $SERVICE && \
        echo OK
    " 2>&1 && break
    echo "⚠️  远程操作重试 $i..."
    sleep 2
done || { echo "❌ 远程操作失败"; exit 1; }

# 清理交叉编译产物
rm -f gallery gallery-linux

echo ""
echo "✅ 发布完成！"
