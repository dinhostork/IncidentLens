"""Run benchmark scenarios from bench/scenarios/ on the local demo stack.

The runner only makes sense inside a checkout of the repository: it reads the
scenario files, writes fault flags through demo/faults.py and drives the stack
started by `make up`.
"""
