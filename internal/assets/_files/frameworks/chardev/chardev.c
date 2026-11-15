// SPDX-License-Identifier: GPL-2.0
/*
 * gokm/chardev glue: misc character devices backed by Go
 * (lib/chardev/chardev.go). Data moves through kernel bounce buffers, so
 * user-memory faults never happen while holding the Go lock.
 */
#include <linux/module.h>
#include <linux/fs.h>
#include <linux/miscdevice.h>
#include <linux/slab.h>
#include <linux/string.h>
#include <linux/uaccess.h>
#include <linux/minmax.h>

#include "gokm.h"

#define GOKM_CHARDEV_MAX_IO	(64 * 1024)

/* Implemented in Go. */
long gokm_chardev_go_open(unsigned long dev, unsigned long *file);
s64 gokm_chardev_go_read(unsigned long file, void *buf, long count, s64 off);
s64 gokm_chardev_go_write(unsigned long file, const void *buf, long count, s64 off);
void gokm_chardev_go_release(unsigned long file);
s64 gokm_chardev_go_ioctl(unsigned long file, u32 cmd, unsigned long arg);

/* Called from Go. */
long gokm_chardev_register(const char *name, u32 mode, unsigned long handle,
			   unsigned long *out);
void gokm_chardev_deregister(unsigned long ptr);
long gokm_chardev_copy_from_user(void *dst, unsigned long uaddr, long n);
long gokm_chardev_copy_to_user(unsigned long uaddr, const void *src, long n);

struct gokm_chardev {
	struct miscdevice misc;
	unsigned long handle;	/* kernel.Handle of the Go Device */
};

static int gokm_chardev_open(struct inode *inode, struct file *file)
{
	/* misc_open() sets private_data to our miscdevice. */
	struct gokm_chardev *d = container_of(file->private_data,
					      struct gokm_chardev, misc);
	unsigned long fh = 0;
	long err;

	GOKM_ENTER();
	err = gokm_chardev_go_open(d->handle, &fh);
	GOKM_LEAVE();
	if (err)
		return err;
	file->private_data = (void *)fh;
	return 0;
}

static ssize_t gokm_chardev_read(struct file *file, char __user *ubuf, size_t count,
				 loff_t *ppos)
{
	void *buf;
	s64 n;

	count = min_t(size_t, count, GOKM_CHARDEV_MAX_IO);
	if (!count)
		return 0;
	buf = kvmalloc(count, GFP_KERNEL);
	if (!buf)
		return -ENOMEM;

	GOKM_ENTER();
	n = gokm_chardev_go_read((unsigned long)file->private_data, buf, count, *ppos);
	GOKM_LEAVE();

	if (n > (s64)count)
		n = -EIO;
	if (n > 0) {
		if (copy_to_user(ubuf, buf, n))
			n = -EFAULT;
		else
			*ppos += n;
	}
	kvfree(buf);
	return n;
}

static ssize_t gokm_chardev_write(struct file *file, const char __user *ubuf,
				  size_t count, loff_t *ppos)
{
	void *buf;
	s64 n;

	count = min_t(size_t, count, GOKM_CHARDEV_MAX_IO);
	if (!count)
		return 0;
	buf = memdup_user(ubuf, count);
	if (IS_ERR(buf))
		return PTR_ERR(buf);

	GOKM_ENTER();
	n = gokm_chardev_go_write((unsigned long)file->private_data, buf, count, *ppos);
	GOKM_LEAVE();

	kfree(buf);
	if (n > (s64)count)
		n = count;
	if (n > 0)
		*ppos += n;
	return n;
}

static long gokm_chardev_ioctl(struct file *file, unsigned int cmd, unsigned long arg)
{
	s64 ret;

	GOKM_ENTER();
	ret = gokm_chardev_go_ioctl((unsigned long)file->private_data, cmd, arg);
	GOKM_LEAVE();
	return ret;
}

long gokm_chardev_copy_from_user(void *dst, unsigned long uaddr, long n)
{
	return copy_from_user(dst, (const void __user *)uaddr, n) ? -EFAULT : 0;
}

long gokm_chardev_copy_to_user(unsigned long uaddr, const void *src, long n)
{
	return copy_to_user((void __user *)uaddr, src, n) ? -EFAULT : 0;
}

static int gokm_chardev_release(struct inode *inode, struct file *file)
{
	GOKM_ENTER();
	gokm_chardev_go_release((unsigned long)file->private_data);
	GOKM_LEAVE();
	return 0;
}

static const struct file_operations gokm_chardev_fops = {
	.owner		= THIS_MODULE,
	.open		= gokm_chardev_open,
	.read		= gokm_chardev_read,
	.write		= gokm_chardev_write,
	.release	= gokm_chardev_release,
	.unlocked_ioctl	= gokm_chardev_ioctl,
	.compat_ioctl	= compat_ptr_ioctl,
	.llseek		= default_llseek,
};

long gokm_chardev_register(const char *name, u32 mode, unsigned long handle,
			   unsigned long *out)
{
	struct gokm_chardev *d;
	int err;

	d = kzalloc(sizeof(*d), GFP_KERNEL);
	if (!d)
		return -ENOMEM;
	d->misc.name = kstrdup(name, GFP_KERNEL);
	if (!d->misc.name) {
		kfree(d);
		return -ENOMEM;
	}
	d->misc.minor = MISC_DYNAMIC_MINOR;
	d->misc.fops = &gokm_chardev_fops;
	d->misc.mode = mode;
	d->handle = handle;

	err = misc_register(&d->misc);
	if (err) {
		kfree(d->misc.name);
		kfree(d);
		return err;
	}
	*out = (unsigned long)d;
	return 0;
}

void gokm_chardev_deregister(unsigned long ptr)
{
	struct gokm_chardev *d = (struct gokm_chardev *)ptr;

	GOKM_PARK();
	misc_deregister(&d->misc);
	GOKM_UNPARK();
	kfree(d->misc.name);
	kfree(d);
}
