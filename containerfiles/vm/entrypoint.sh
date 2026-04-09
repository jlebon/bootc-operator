#!/bin/bash

/usr/sbin/virtlogd &
/usr/bin/virtstoraged &
/usr/sbin/virtnetworkd -d
/usr/sbin/virtqemud -v -t 0
