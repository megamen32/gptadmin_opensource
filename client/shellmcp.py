#!/usr/bin/env python3
"""DEPRECATED legacy Python ShellMCP entrypoint.

Use go-shellmcp / rootd-go-canary for the primary GPTAdmin shell transport.
This module remains only as a compatibility shim for old installs that still
invoke shellmcp.py directly.
"""
from rootd import *  # noqa: F401,F403
