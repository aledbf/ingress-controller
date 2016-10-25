#!/bin/bash

sed -e 's/^  *$/\'$'\n/g' | sed -e '/^$/{N;/^\n$/d;}'
