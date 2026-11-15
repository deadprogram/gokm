// SPDX-License-Identifier: GPL-2.0
/*
 * gokm/procfs glue: /proc files backed by Go (lib/procfs/procfs.go).
 *
 * open() asks Go for the content and keeps a kernel copy per open file, so
 * reads never touch Go memory (or hold the Go lock while faulting on user
 * memory). Writes are copied into a kernel buffer before calling Go.
 */
#include <linux/module.h>
#include <linux/fs.h>
#include <linux/proc_fs.h>
#include <linux/slab.h>
#include <linux/string.h>
#include <linux/uaccess.h>

#include "gokm.h"

#define GOKM_PROCFS_MAX_WRITE	(64 * 1024)

/* Implemented in Go. */
long gokm_procfs_go_open(unsigned long h, const u8 **buf, long *len);
long gokm_procfs_go_write(unsigned long h, const void *buf, long len);

/* Called from Go. */
long gokm_procfs_create(const char *name, u32 mode, unsigned long handle, unsigned long *out);
void gokm_procfs_remove(unsigned long ptr);

struct gokm_procfs_content {
	size_t len;
	char data[];
};

static int gokm_procfs_open(struct inode *inode, struct file *file)
{
	unsigned long h = (unsigned long)pde_data(inode);
	struct gokm_procfs_content *c = NULL;
	const u8 *buf = NULL;
	long len = 0, err;

	GOKM_ENTER();
	err = gokm_procfs_go_open(h, &buf, &len);
	if (!err) {
		c = kvmalloc(sizeof(*c) + len, GFP_KERNEL);
		if (c) {
			c->len = len;
			if (len)
				memcpy(c->data, buf, len);	/* before leaving Go */
		} else {
			err = -ENOMEM;
		}
	}
	GOKM_LEAVE();
	if (err)
		return err;
	file->private_data = c;
	return 0;
}

static ssize_t gokm_procfs_read(struct file *file, char __user *ubuf, size_t count,
				loff_t *ppos)
{
	struct gokm_procfs_content *c = file->private_data;

	return simple_read_from_buffer(ubuf, count, ppos, c->data, c->len);
}

static ssize_t gokm_procfs_write(struct file *file, const char __user *ubuf,
				 size_t count, loff_t *ppos)
{
	unsigned long h = (unsigned long)pde_data(file_inode(file));
	void *kbuf;
	long err;

	if (count > GOKM_PROCFS_MAX_WRITE)
		count = GOKM_PROCFS_MAX_WRITE;
	kbuf = memdup_user(ubuf, count);
	if (IS_ERR(kbuf))
		return PTR_ERR(kbuf);

	GOKM_ENTER();
	err = gokm_procfs_go_write(h, kbuf, count);
	GOKM_LEAVE();
	kfree(kbuf);
	return err ? err : count;
}

static int gokm_procfs_release(struct inode *inode, struct file *file)
{
	kvfree(file->private_data);
	return 0;
}

static const struct proc_ops gokm_procfs_ops = {
	.proc_open	= gokm_procfs_open,
	.proc_read	= gokm_procfs_read,
	.proc_write	= gokm_procfs_write,
	.proc_release	= gokm_procfs_release,
	.proc_lseek	= default_llseek,
};

long gokm_procfs_create(const char *name, u32 mode, unsigned long handle, unsigned long *out)
{
	struct proc_dir_entry *pde;

	pde = proc_create_data(name, mode, NULL, &gokm_procfs_ops, (void *)handle);
	if (!pde)
		return -ENOMEM;	/* also returned for an existing name */
	*out = (unsigned long)pde;
	return 0;
}

void gokm_procfs_remove(unsigned long ptr)
{
	/* Waits for in-progress opens, which may be waiting to enter Go. */
	GOKM_PARK();
	proc_remove((struct proc_dir_entry *)ptr);
	GOKM_UNPARK();
}
