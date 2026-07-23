#!/bin/bash
# 给每行输出加上时间戳
while IFS= read -r line; do
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $line"
done
