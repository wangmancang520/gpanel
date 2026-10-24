#!/bin/bash
# Stand-in "game server" for smoke tests: echoes console input,
# exits 0 on "stop" and 3 on "crash".
echo ready
while read -r line; do
  echo "got $line"
  [ "$line" = stop ] && exit 0
  [ "$line" = crash ] && exit 3
done
