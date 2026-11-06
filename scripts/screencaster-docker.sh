#!/bin/sh
# screencaster-docker: run screencaster in its Docker image, with nothing
# installed on this machine but Docker. Same arguments as `screencaster`:
#
#   ./screencaster-docker render demos/my-demo.yaml
#
# The current folder is mounted at /work, so demos and outputs live there. This
# file is a template: the release workflow fills in the image tag's version
# (decision 77). The wrapper ships alone (no sibling files), so the Chromium
# sandbox's seccomp profile is embedded below rather than referenced by path
# (decision 83; see README "Chromium sandbox" and ARCHITECTURE §14 for the
# pinned moby/profiles commit the profile started from, plus the one rule
# added on top for Chromium's own unprivileged namespace creation).
set -eu

seccomp_profile="$(mktemp)"
cleanup() { rm -f "$seccomp_profile"; }
trap cleanup EXIT

cat > "$seccomp_profile" <<'SECCOMP_PROFILE_EOF'
{
	"defaultAction": "SCMP_ACT_ERRNO",
	"defaultErrnoRet": 1,
	"archMap": [
		{
			"architecture": "SCMP_ARCH_X86_64",
			"subArchitectures": [
				"SCMP_ARCH_X86",
				"SCMP_ARCH_X32"
			]
		},
		{
			"architecture": "SCMP_ARCH_AARCH64",
			"subArchitectures": [
				"SCMP_ARCH_ARM"
			]
		},
		{
			"architecture": "SCMP_ARCH_MIPS64",
			"subArchitectures": [
				"SCMP_ARCH_MIPS",
				"SCMP_ARCH_MIPS64N32"
			]
		},
		{
			"architecture": "SCMP_ARCH_MIPS64N32",
			"subArchitectures": [
				"SCMP_ARCH_MIPS",
				"SCMP_ARCH_MIPS64"
			]
		},
		{
			"architecture": "SCMP_ARCH_MIPSEL64",
			"subArchitectures": [
				"SCMP_ARCH_MIPSEL",
				"SCMP_ARCH_MIPSEL64N32"
			]
		},
		{
			"architecture": "SCMP_ARCH_MIPSEL64N32",
			"subArchitectures": [
				"SCMP_ARCH_MIPSEL",
				"SCMP_ARCH_MIPSEL64"
			]
		},
		{
			"architecture": "SCMP_ARCH_S390X",
			"subArchitectures": [
				"SCMP_ARCH_S390"
			]
		},
		{
			"architecture": "SCMP_ARCH_RISCV64",
			"subArchitectures": null
		},
		{
			"architecture": "SCMP_ARCH_LOONGARCH64",
			"subArchitectures": null
		}
	],
	"syscalls": [
		{
			"names": [
				"accept",
				"accept4",
				"access",
				"adjtimex",
				"alarm",
				"bind",
				"brk",
				"cachestat",
				"capget",
				"capset",
				"chdir",
				"chmod",
				"chown",
				"chown32",
				"clock_adjtime",
				"clock_adjtime64",
				"clock_getres",
				"clock_getres_time64",
				"clock_gettime",
				"clock_gettime64",
				"clock_nanosleep",
				"clock_nanosleep_time64",
				"close",
				"close_range",
				"connect",
				"copy_file_range",
				"creat",
				"dup",
				"dup2",
				"dup3",
				"epoll_create",
				"epoll_create1",
				"epoll_ctl",
				"epoll_ctl_old",
				"epoll_pwait",
				"epoll_pwait2",
				"epoll_wait",
				"epoll_wait_old",
				"eventfd",
				"eventfd2",
				"execve",
				"execveat",
				"exit",
				"exit_group",
				"faccessat",
				"faccessat2",
				"fadvise64",
				"fadvise64_64",
				"fallocate",
				"fanotify_mark",
				"fchdir",
				"fchmod",
				"fchmodat",
				"fchmodat2",
				"fchown",
				"fchown32",
				"fchownat",
				"fcntl",
				"fcntl64",
				"fdatasync",
				"fgetxattr",
				"flistxattr",
				"flock",
				"fork",
				"fremovexattr",
				"fsetxattr",
				"fstat",
				"fstat64",
				"fstatat64",
				"fstatfs",
				"fstatfs64",
				"fsync",
				"ftruncate",
				"ftruncate64",
				"futex",
				"futex_requeue",
				"futex_time64",
				"futex_wait",
				"futex_waitv",
				"futex_wake",
				"futimesat",
				"getcpu",
				"getcwd",
				"getdents",
				"getdents64",
				"getegid",
				"getegid32",
				"geteuid",
				"geteuid32",
				"getgid",
				"getgid32",
				"getgroups",
				"getgroups32",
				"getitimer",
				"getpeername",
				"getpgid",
				"getpgrp",
				"getpid",
				"getppid",
				"getpriority",
				"getrandom",
				"getresgid",
				"getresgid32",
				"getresuid",
				"getresuid32",
				"getrlimit",
				"get_robust_list",
				"getrusage",
				"getsid",
				"getsockname",
				"getsockopt",
				"get_thread_area",
				"gettid",
				"gettimeofday",
				"getuid",
				"getuid32",
				"getxattr",
				"getxattrat",
				"inotify_add_watch",
				"inotify_init",
				"inotify_init1",
				"inotify_rm_watch",
				"io_cancel",
				"ioctl",
				"io_destroy",
				"io_getevents",
				"io_pgetevents",
				"io_pgetevents_time64",
				"ioprio_get",
				"ioprio_set",
				"io_setup",
				"io_submit",
				"ipc",
				"kill",
				"landlock_add_rule",
				"landlock_create_ruleset",
				"landlock_restrict_self",
				"lchown",
				"lchown32",
				"lgetxattr",
				"link",
				"linkat",
				"listen",
				"listmount",
				"listxattr",
				"listxattrat",
				"llistxattr",
				"_llseek",
				"lremovexattr",
				"lseek",
				"lsetxattr",
				"lstat",
				"lstat64",
				"madvise",
				"map_shadow_stack",
				"membarrier",
				"memfd_create",
				"memfd_secret",
				"mincore",
				"mkdir",
				"mkdirat",
				"mknod",
				"mknodat",
				"mlock",
				"mlock2",
				"mlockall",
				"mmap",
				"mmap2",
				"mprotect",
				"mq_getsetattr",
				"mq_notify",
				"mq_open",
				"mq_timedreceive",
				"mq_timedreceive_time64",
				"mq_timedsend",
				"mq_timedsend_time64",
				"mq_unlink",
				"mremap",
				"mseal",
				"msgctl",
				"msgget",
				"msgrcv",
				"msgsnd",
				"msync",
				"munlock",
				"munlockall",
				"munmap",
				"name_to_handle_at",
				"nanosleep",
				"newfstatat",
				"_newselect",
				"open",
				"openat",
				"openat2",
				"pause",
				"pidfd_open",
				"pidfd_send_signal",
				"pipe",
				"pipe2",
				"pkey_alloc",
				"pkey_free",
				"pkey_mprotect",
				"poll",
				"ppoll",
				"ppoll_time64",
				"prctl",
				"pread64",
				"preadv",
				"preadv2",
				"prlimit64",
				"process_mrelease",
				"pselect6",
				"pselect6_time64",
				"pwrite64",
				"pwritev",
				"pwritev2",
				"read",
				"readahead",
				"readlink",
				"readlinkat",
				"readv",
				"recv",
				"recvfrom",
				"recvmmsg",
				"recvmmsg_time64",
				"recvmsg",
				"remap_file_pages",
				"removexattr",
				"removexattrat",
				"rename",
				"renameat",
				"renameat2",
				"restart_syscall",
				"riscv_hwprobe",
				"rmdir",
				"rseq",
				"rt_sigaction",
				"rt_sigpending",
				"rt_sigprocmask",
				"rt_sigqueueinfo",
				"rt_sigreturn",
				"rt_sigsuspend",
				"rt_sigtimedwait",
				"rt_sigtimedwait_time64",
				"rt_tgsigqueueinfo",
				"sched_getaffinity",
				"sched_getattr",
				"sched_getparam",
				"sched_get_priority_max",
				"sched_get_priority_min",
				"sched_getscheduler",
				"sched_rr_get_interval",
				"sched_rr_get_interval_time64",
				"sched_setaffinity",
				"sched_setattr",
				"sched_setparam",
				"sched_setscheduler",
				"sched_yield",
				"seccomp",
				"select",
				"semctl",
				"semget",
				"semop",
				"semtimedop",
				"semtimedop_time64",
				"send",
				"sendfile",
				"sendfile64",
				"sendmmsg",
				"sendmsg",
				"sendto",
				"setfsgid",
				"setfsgid32",
				"setfsuid",
				"setfsuid32",
				"setgid",
				"setgid32",
				"setgroups",
				"setgroups32",
				"setitimer",
				"setpgid",
				"setpriority",
				"setregid",
				"setregid32",
				"setresgid",
				"setresgid32",
				"setresuid",
				"setresuid32",
				"setreuid",
				"setreuid32",
				"setrlimit",
				"set_robust_list",
				"setsid",
				"setsockopt",
				"set_thread_area",
				"set_tid_address",
				"setuid",
				"setuid32",
				"setxattr",
				"setxattrat",
				"shmat",
				"shmctl",
				"shmdt",
				"shmget",
				"shutdown",
				"sigaltstack",
				"signalfd",
				"signalfd4",
				"sigprocmask",
				"sigreturn",
				"socketcall",
				"socketpair",
				"splice",
				"stat",
				"stat64",
				"statfs",
				"statfs64",
				"statmount",
				"statx",
				"symlink",
				"symlinkat",
				"sync",
				"sync_file_range",
				"syncfs",
				"sysinfo",
				"tee",
				"tgkill",
				"time",
				"timer_create",
				"timer_delete",
				"timer_getoverrun",
				"timer_gettime",
				"timer_gettime64",
				"timer_settime",
				"timer_settime64",
				"timerfd_create",
				"timerfd_gettime",
				"timerfd_gettime64",
				"timerfd_settime",
				"timerfd_settime64",
				"times",
				"tkill",
				"truncate",
				"truncate64",
				"ugetrlimit",
				"umask",
				"uname",
				"unlink",
				"unlinkat",
				"uretprobe",
				"utime",
				"utimensat",
				"utimensat_time64",
				"utimes",
				"vfork",
				"vmsplice",
				"wait4",
				"waitid",
				"waitpid",
				"write",
				"writev"
			],
			"action": "SCMP_ACT_ALLOW"
		},
		{
			"names": [
				"process_vm_readv",
				"process_vm_writev",
				"ptrace"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"minKernel": "4.8"
			}
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 3,
					"op": "SCMP_CMP_LT"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 7,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 8,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 9,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 10,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 13,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 14,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 15,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 16,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 17,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 18,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 21,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 22,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 24,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 26,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 27,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 28,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 29,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 30,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 31,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 32,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 33,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 35,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 36,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 39,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 41,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 42,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 43,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 44,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"socket"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 45,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"personality"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 0,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"personality"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 8,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"personality"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 131072,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"personality"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 131080,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"personality"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 4294967295,
					"op": "SCMP_CMP_EQ"
				}
			]
		},
		{
			"names": [
				"sync_file_range2",
				"swapcontext"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"arches": [
					"ppc64le"
				]
			}
		},
		{
			"names": [
				"arm_fadvise64_64",
				"arm_sync_file_range",
				"sync_file_range2",
				"breakpoint",
				"cacheflush",
				"set_tls"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"arches": [
					"arm",
					"arm64"
				]
			}
		},
		{
			"names": [
				"arch_prctl"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"arches": [
					"amd64",
					"x32"
				]
			}
		},
		{
			"names": [
				"modify_ldt"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"arches": [
					"amd64",
					"x32",
					"x86"
				]
			}
		},
		{
			"names": [
				"s390_pci_mmio_read",
				"s390_pci_mmio_write",
				"s390_runtime_instr"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"arches": [
					"s390",
					"s390x"
				]
			}
		},
		{
			"names": [
				"riscv_flush_icache"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"arches": [
					"riscv64"
				]
			}
		},
		{
			"names": [
				"open_by_handle_at"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_DAC_READ_SEARCH"
				]
			}
		},
		{
			"names": [
				"bpf",
				"clone",
				"clone3",
				"fanotify_init",
				"fsconfig",
				"fsmount",
				"fsopen",
				"fspick",
				"lookup_dcookie",
				"lsm_get_self_attr",
				"lsm_list_modules",
				"lsm_set_self_attr",
				"mount",
				"mount_setattr",
				"move_mount",
				"open_tree",
				"perf_event_open",
				"quotactl",
				"quotactl_fd",
				"setdomainname",
				"sethostname",
				"setns",
				"syslog",
				"umount",
				"umount2",
				"unshare"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_SYS_ADMIN"
				]
			}
		},
		{
			"names": [
				"clone"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 2114060288,
					"op": "SCMP_CMP_MASKED_EQ"
				}
			],
			"excludes": {
				"caps": [
					"CAP_SYS_ADMIN"
				],
				"arches": [
					"s390",
					"s390x"
				]
			}
		},
		{
			"names": [
				"clone"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 1,
					"value": 2114060288,
					"op": "SCMP_CMP_MASKED_EQ"
				}
			],
			"comment": "s390 parameter ordering for clone is different",
			"includes": {
				"arches": [
					"s390",
					"s390x"
				]
			},
			"excludes": {
				"caps": [
					"CAP_SYS_ADMIN"
				]
			}
		},
		{
			"names": [
				"clone3"
			],
			"action": "SCMP_ACT_ERRNO",
			"errnoRet": 38,
			"excludes": {
				"caps": [
					"CAP_SYS_ADMIN"
				]
			}
		},
		{
			"names": [
				"reboot"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_SYS_BOOT"
				]
			}
		},
		{
			"names": [
				"chroot"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_SYS_CHROOT"
				]
			}
		},
		{
			"names": [
				"delete_module",
				"init_module",
				"finit_module"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_SYS_MODULE"
				]
			}
		},
		{
			"names": [
				"acct"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_SYS_PACCT"
				]
			}
		},
		{
			"names": [
				"kcmp",
				"pidfd_getfd",
				"process_madvise",
				"process_vm_readv",
				"process_vm_writev",
				"ptrace"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_SYS_PTRACE"
				]
			}
		},
		{
			"names": [
				"iopl",
				"ioperm"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_SYS_RAWIO"
				]
			}
		},
		{
			"names": [
				"settimeofday",
				"stime",
				"clock_settime",
				"clock_settime64"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_SYS_TIME"
				]
			}
		},
		{
			"names": [
				"vhangup"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_SYS_TTY_CONFIG"
				]
			}
		},
		{
			"names": [
				"get_mempolicy",
				"mbind",
				"set_mempolicy",
				"set_mempolicy_home_node"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_SYS_NICE"
				]
			}
		},
		{
			"names": [
				"syslog"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_SYSLOG"
				]
			}
		},
		{
			"names": [
				"bpf"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_BPF"
				]
			}
		},
		{
			"names": [
				"perf_event_open"
			],
			"action": "SCMP_ACT_ALLOW",
			"includes": {
				"caps": [
					"CAP_PERFMON"
				]
			}
		},
		{
			"names": [
				"clone",
				"unshare",
				"setns"
			],
			"action": "SCMP_ACT_ALLOW",
			"args": [
				{
					"index": 0,
					"value": 2180906752,
					"valueTwo": 0,
					"op": "SCMP_CMP_MASKED_EQ"
				}
			],
			"excludes": {
				"caps": [
					"CAP_SYS_ADMIN"
				]
			},
			"comment": "Addition on top of the moby/profiles copy above (decision 83, M1 fix-up): the stock clone() carve-out above only allows an unprivileged (no CAP_SYS_ADMIN) process to fork/thread when NONE of CLONE_NEWNS/NEWCGROUP/NEWUTS/NEWIPC/NEWUSER/NEWPID/NEWNET are set; every namespace-creating syscall stays CAP_SYS_ADMIN-only otherwise. Chromium's own unprivileged sandbox creates its user/pid/net/mount/ipc/uts/cgroup namespaces via several clone()/unshare()/setns() calls, each setting one or more of those flags and nothing else (observed by strace against the shipped build: CLONE_NEWUSER, then CLONE_NEWPID, then CLONE_NEWUSER|CLONE_NEWPID|CLONE_NEWNET together), and the stock profile denies every one of them, independent of the AppArmor unprivileged-userns restriction. This rule allows exactly that and nothing more: value 2180906752 (0x81FDFF00) is every flag bit except CSIGNAL and those same 7 namespace flags; SCMP_CMP_MASKED_EQ with valueTwo 0 passes only when none of those other bits are set, so any mix of just the 7 namespace flags (plus a signal number) matches, while CAP_SYS_ADMIN-gated syscalls (mount, bpf, ptrace, ...) and any other clone()/unshare()/setns() flag stay denied. The rest of this file is unchanged from the pinned commit above."
		}
	]
}
SECCOMP_PROFILE_EOF

# --init forwards SIGTERM to the container's own init, which forwards it to the
# app (no exec here: we need to run after docker exits, to remove the temp
# profile). Chromium starts without /dev/shm, and an app on this machine is
# reached by its bridge IP, written into the demo's goto URLs (decision 79).
# --user runs the container as this user, so the rendered files are not
# root-owned. --security-opt seccomp loads the Chromium sandbox profile above
# (decision 83); without it, render fails with the Chromium sandbox error.
docker run --rm --init \
  --user "$(id -u):$(id -g)" \
  --security-opt "seccomp=$seccomp_profile" \
  -v "$PWD:/work" -w /work \
  --entrypoint screencaster \
  ghcr.io/makuchpatryk/screencaster:@VERSION@ "$@"
