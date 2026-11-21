// SPDX-License-Identifier: GPL-2.0
/*
 * gokm/fs glue: VFS callbacks -> Go (lib/fs/fs.go). Compiled into a module
 * when it imports gokm/fs.
 *
 * Each mount's Go state is a kernel.Handle in sb->s_fs_info; inodes are
 * identified by inode number, mapped to Go nodes on the Go side. File data
 * moves through kernel bounce buffers, so user-memory faults never happen
 * while in Go. VFS locks (i_rwsem) are always taken before entering Go.
 *
 * read()/write() go straight to Go, without the page cache. mmap() uses the
 * page cache (read_folio/writepages call Go's ReadAt/WriteAt); to keep the
 * two coherent, read and write first write back dirty cached pages of their
 * range, and write and truncate then drop the cached pages they changed.
 * All of that happens outside Go: Go code never waits for page-cache state.
 *
 * Extended attributes: user.* and trusted.* names go to the node's Xattrer
 * (the VFS checks permissions; trusted.* needs CAP_SYS_ADMIN).
 */
#include <linux/module.h>
#include <linux/fs.h>
#include <linux/fs_context.h>
#include <linux/pagemap.h>
#include <linux/uio.h>
#include <linux/err.h>
#include <linux/minmax.h>
#include <linux/slab.h>
#include <linux/string.h>
#include <linux/buffer_head.h>
#include <linux/blkdev.h>
#include <linux/version.h>
#include <linux/math64.h>
#include <linux/statfs.h>
#include <linux/writeback.h>
#include <linux/xattr.h>
#include <linux/highmem.h>
#include <linux/mm.h>

#include "gokm.h"

#define GOKM_FS_ROOT_INO	1
#define GOKM_FS_MAX_IO		(128 * 1024)

/* Implemented in Go (lib/fs/fs.go). Go int is 64-bit: long. */
long gokm_fs_go_mount(unsigned long fs, unsigned long sb, long block_size, s64 size,
		      bool read_only, unsigned long *out);
void gokm_fs_go_unmount(unsigned long m);
void gokm_fs_go_release(unsigned long m);
long gokm_fs_go_stat(unsigned long m, u64 ino, u32 *mode, s64 *size,
		     s64 *atime, s64 *mtime, s64 *ctime);
long gokm_fs_go_statfs(unsigned long m, s64 *bsize, s64 *blocks, s64 *bfree, s64 *bavail,
		       s64 *files, s64 *ffree, s64 *namelen);
long gokm_fs_go_sync(unsigned long m);
long gokm_fs_go_settimes(unsigned long m, u64 ino, s64 atime, s64 mtime);
s64 gokm_fs_go_lookup(unsigned long m, u64 dir, const unsigned char *name, long len);
s64 gokm_fs_go_create(unsigned long m, u64 dir, const unsigned char *name, long len, u32 mode);
long gokm_fs_go_remove(unsigned long m, u64 dir, const unsigned char *name, long len, bool is_dir);
void gokm_fs_go_evict(unsigned long m, u64 ino);
s64 gokm_fs_go_read(unsigned long m, u64 ino, void *buf, long count, s64 off);
s64 gokm_fs_go_write(unsigned long m, u64 ino, const void *buf, long count, s64 off);
long gokm_fs_go_truncate(unsigned long m, u64 ino, s64 size);
long gokm_fs_go_iterate(unsigned long m, u64 dir, s64 start, unsigned long ctx);
long gokm_fs_go_rename(unsigned long m, u64 old_dir, const unsigned char *old_name, long old_len,
		       u64 new_dir, const unsigned char *new_name, long new_len);
s64 gokm_fs_go_symlink(unsigned long m, u64 dir, const unsigned char *name, long len,
		       const char *target, long target_len);
long gokm_fs_go_readlink(unsigned long m, u64 ino, const char **buf, long *len);
long gokm_fs_go_getxattr(unsigned long m, u64 ino, const char *name, long name_len,
			 void *buf, long size);
long gokm_fs_go_setxattr(unsigned long m, u64 ino, const char *name, long name_len,
			 const void *value, long size, bool remove, s32 flags);
long gokm_fs_go_listxattr(unsigned long m, u64 ino, char *buf, long size);

/* Called from Go. */
long gokm_fs_register(const char *name, long len, u32 magic, bool requires_dev,
		      s64 max_bytes, unsigned long handle, unsigned long *out);
long gokm_fs_dev_read(unsigned long sbp, u64 block, void *dst, long nblocks);
long gokm_fs_dev_write(unsigned long sbp, u64 block, const void *src, long nblocks);
long gokm_fs_dev_sync(unsigned long sbp);
long gokm_fs_dev_set_blocksize(unsigned long sbp, long size);
void gokm_fs_unregister(unsigned long ptr);
bool gokm_fs_emit(unsigned long ctx, const char *name, long len, u64 ino, u32 dtype);

struct gokm_fs_type {
	struct file_system_type type;
	unsigned long handle;	/* kernel.Handle of the Go FileSystem */
	u32 magic;
	bool requires_dev;	/* block-device filesystem (MountDevice) */
	s64 max_bytes;
};

static const struct inode_operations gokm_fs_dir_iops, gokm_fs_file_iops, gokm_fs_symlink_iops;
static const struct file_operations gokm_fs_dir_fops, gokm_fs_file_fops;
static const struct super_operations gokm_fs_sops;
static const struct address_space_operations gokm_fs_aops;

static inline unsigned long gokm_fs_mount(struct super_block *sb)
{
	return (unsigned long)sb->s_fs_info;
}

static struct timespec64 gokm_fs_ts(s64 ns)
{
	s32 rem;
	s64 sec = div_s64_rem(ns, NSEC_PER_SEC, &rem);

	if (rem < 0) {
		sec--;
		rem += NSEC_PER_SEC;
	}
	return (struct timespec64){ .tv_sec = sec, .tv_nsec = rem };
}

/* Fill an inode's mode/size/timestamps from Go. */
static int gokm_fs_refresh(struct inode *inode, bool init)
{
	s64 size, atime = 0, mtime = 0, ctime = 0;
	u32 mode;
	long err;

	GOKM_ENTER();
	err = gokm_fs_go_stat(gokm_fs_mount(inode->i_sb), inode->i_ino, &mode, &size,
			      &atime, &mtime, &ctime);
	GOKM_LEAVE();
	if (err)
		return err;
	if (init) {
		inode->i_mode = mode;
		simple_inode_init_ts(inode);
	}
	if (S_ISREG(inode->i_mode) || S_ISLNK(inode->i_mode))
		i_size_write(inode, size);
	if (atime)
		inode_set_atime_to_ts(inode, gokm_fs_ts(atime));
	if (mtime)
		inode_set_mtime_to_ts(inode, gokm_fs_ts(mtime));
	if (ctime)
		inode_set_ctime_to_ts(inode, gokm_fs_ts(ctime));
	return 0;
}

static s64 gokm_fs_ns(struct timespec64 ts)
{
	return timespec64_to_ns(&ts);
}

static struct inode *gokm_fs_iget(struct super_block *sb, u64 ino)
{
	struct inode *inode;
	int err;

	inode = iget_locked(sb, ino);
	if (!inode)
		return ERR_PTR(-ENOMEM);
	if (!(inode_state_read_once(inode) & I_NEW))
		return inode;

	err = gokm_fs_refresh(inode, true);
	if (err) {
		iget_failed(inode);
		return ERR_PTR(err);
	}
	if (S_ISDIR(inode->i_mode)) {
		inode->i_op = &gokm_fs_dir_iops;
		inode->i_fop = &gokm_fs_dir_fops;
		set_nlink(inode, 2);
	} else if (S_ISLNK(inode->i_mode)) {
		inode->i_op = &gokm_fs_symlink_iops;
		set_nlink(inode, 1);
	} else {
		inode->i_op = &gokm_fs_file_iops;
		inode->i_fop = &gokm_fs_file_fops;
		inode->i_mapping->a_ops = &gokm_fs_aops;
		set_nlink(inode, 1);
	}
	unlock_new_inode(inode);
	return inode;
}

/* ---- Directories ----------------------------------------------------- */

static struct dentry *gokm_fs_lookup(struct inode *dir, struct dentry *dentry,
				     unsigned int flags)
{
	struct inode *inode = NULL;
	s64 ino;

	if (dentry->d_name.len > NAME_MAX)
		return ERR_PTR(-ENAMETOOLONG);

	GOKM_ENTER();
	ino = gokm_fs_go_lookup(gokm_fs_mount(dir->i_sb), dir->i_ino,
				dentry->d_name.name, dentry->d_name.len);
	GOKM_LEAVE();
	if (ino < 0)
		return ERR_PTR(ino);
	if (ino) {
		inode = gokm_fs_iget(dir->i_sb, ino);
		if (IS_ERR(inode))
			return ERR_CAST(inode);
	}
	return d_splice_alias(inode, dentry);
}

static int gokm_fs_new_node(struct mnt_idmap *idmap, struct inode *dir,
			    struct dentry *dentry, umode_t mode)
{
	struct inode *inode;
	s64 ino;

	GOKM_ENTER();
	ino = gokm_fs_go_create(gokm_fs_mount(dir->i_sb), dir->i_ino,
				dentry->d_name.name, dentry->d_name.len, mode);
	GOKM_LEAVE();
	if (ino < 0)
		return ino;

	inode = gokm_fs_iget(dir->i_sb, ino);
	if (IS_ERR(inode))
		return PTR_ERR(inode);
	inode_init_owner(idmap, inode, dir, mode);
	inode_set_mtime_to_ts(dir, inode_set_ctime_current(dir));
	d_instantiate(dentry, inode);
	return 0;
}

/* ->create lost its excl argument in Linux 7.3 (torvalds/linux b2f1e6301efa). */
#if LINUX_VERSION_CODE >= KERNEL_VERSION(7, 3, 0)
static int gokm_fs_create(struct mnt_idmap *idmap, struct inode *dir,
			  struct dentry *dentry, umode_t mode)
#else
static int gokm_fs_create(struct mnt_idmap *idmap, struct inode *dir,
			  struct dentry *dentry, umode_t mode, bool excl)
#endif
{
	return gokm_fs_new_node(idmap, dir, dentry, (mode & ~S_IFMT) | S_IFREG);
}

static struct dentry *gokm_fs_mkdir(struct mnt_idmap *idmap, struct inode *dir,
				    struct dentry *dentry, umode_t mode)
{
	int err = gokm_fs_new_node(idmap, dir, dentry, (mode & ~S_IFMT) | S_IFDIR);

	return err ? ERR_PTR(err) : NULL;
}

static int gokm_fs_remove(struct inode *dir, struct dentry *dentry, bool is_dir)
{
	struct inode *inode = d_inode(dentry);
	long err;

	GOKM_ENTER();
	err = gokm_fs_go_remove(gokm_fs_mount(dir->i_sb), dir->i_ino,
				dentry->d_name.name, dentry->d_name.len, is_dir);
	GOKM_LEAVE();
	if (err)
		return err;

	inode_set_mtime_to_ts(dir, inode_set_ctime_current(dir));
	inode_set_ctime_current(inode);
	/* nlink 0: evicted (and forgotten by Go) on last iput. */
	clear_nlink(inode);
	return 0;
}

static int gokm_fs_unlink(struct inode *dir, struct dentry *dentry)
{
	return gokm_fs_remove(dir, dentry, false);
}

static int gokm_fs_rmdir(struct inode *dir, struct dentry *dentry)
{
	return gokm_fs_remove(dir, dentry, true);
}

static int gokm_fs_rename(struct mnt_idmap *idmap, struct inode *old_dir,
			  struct dentry *old_dentry, struct inode *new_dir,
			  struct dentry *new_dentry, unsigned int flags)
{
	struct inode *target = d_inode(new_dentry);
	long err;

	/* RENAME_NOREPLACE is checked by the VFS; exchange/whiteout aren't supported. */
	if (flags & ~RENAME_NOREPLACE)
		return -EINVAL;

	GOKM_ENTER();
	err = gokm_fs_go_rename(gokm_fs_mount(old_dir->i_sb), old_dir->i_ino,
				old_dentry->d_name.name, old_dentry->d_name.len,
				new_dir->i_ino, new_dentry->d_name.name, new_dentry->d_name.len);
	GOKM_LEAVE();
	if (err)
		return err;

	if (target) {
		inode_set_ctime_current(target);
		clear_nlink(target);	/* replaced: evicted on last iput */
	}
	inode_set_mtime_to_ts(old_dir, inode_set_ctime_current(old_dir));
	inode_set_mtime_to_ts(new_dir, inode_set_ctime_current(new_dir));
	inode_set_ctime_current(d_inode(old_dentry));
	return 0;
}

static int gokm_fs_symlink(struct mnt_idmap *idmap, struct inode *dir,
			   struct dentry *dentry, const char *target)
{
	struct inode *inode;
	s64 ino;

	GOKM_ENTER();
	ino = gokm_fs_go_symlink(gokm_fs_mount(dir->i_sb), dir->i_ino,
				 dentry->d_name.name, dentry->d_name.len,
				 target, strlen(target));
	GOKM_LEAVE();
	if (ino < 0)
		return ino;

	inode = gokm_fs_iget(dir->i_sb, ino);
	if (IS_ERR(inode))
		return PTR_ERR(inode);
	inode_init_owner(idmap, inode, dir, S_IFLNK | 0777);
	inode_set_mtime_to_ts(dir, inode_set_ctime_current(dir));
	d_instantiate(dentry, inode);
	return 0;
}

static const char *gokm_fs_get_link(struct dentry *dentry, struct inode *inode,
				    struct delayed_call *done)
{
	const char *buf = NULL;
	char *copy = NULL;
	long len = 0, err;

	if (!dentry)
		return ERR_PTR(-ECHILD);	/* RCU walk: retry in ref-walk mode */

	GOKM_ENTER();
	err = gokm_fs_go_readlink(gokm_fs_mount(inode->i_sb), inode->i_ino, &buf, &len);
	if (!err) {
		copy = kmemdup_nul(buf ? buf : "", len, GFP_KERNEL);
		if (!copy)
			err = -ENOMEM;
	}
	GOKM_LEAVE();
	if (err)
		return ERR_PTR(err);
	set_delayed_call(done, kfree_link, copy);
	return copy;
}

bool gokm_fs_emit(unsigned long ctxp, const char *name, long len, u64 ino, u32 dtype)
{
	struct dir_context *ctx = (struct dir_context *)ctxp;

	if (!dir_emit(ctx, name, len, ino, dtype))
		return false;
	ctx->pos++;
	return true;
}

static int gokm_fs_iterate(struct file *file, struct dir_context *ctx)
{
	struct inode *dir = file_inode(file);
	long err;

	if (!dir_emit_dots(file, ctx))
		return 0;
	GOKM_ENTER();
	err = gokm_fs_go_iterate(gokm_fs_mount(dir->i_sb), dir->i_ino,
				 ctx->pos - 2, (unsigned long)ctx);
	GOKM_LEAVE();
	return err;
}

/* ---- Files ----------------------------------------------------------- */

static ssize_t gokm_fs_read_iter(struct kiocb *iocb, struct iov_iter *to)
{
	struct inode *inode = file_inode(iocb->ki_filp);
	size_t count = min_t(size_t, iov_iter_count(to), GOKM_FS_MAX_IO);
	size_t copied;
	void *buf;
	s64 n;

	if (!count)
		return 0;
	if (inode->i_mapping->nrpages) {	/* dirty mmap data reaches Go first */
		int err = filemap_write_and_wait_range(inode->i_mapping, iocb->ki_pos,
						       iocb->ki_pos + count - 1);
		if (err)
			return err;
	}
	buf = kvmalloc(count, GFP_KERNEL);
	if (!buf)
		return -ENOMEM;

	GOKM_ENTER();
	n = gokm_fs_go_read(gokm_fs_mount(inode->i_sb), inode->i_ino, buf, count,
			    iocb->ki_pos);
	GOKM_LEAVE();

	if (n > (s64)count)
		n = -EIO;
	if (n > 0) {
		copied = copy_to_iter(buf, n, to);
		n = copied ? copied : -EFAULT;
		if (copied)
			iocb->ki_pos += copied;
	}
	kvfree(buf);
	return n;
}

static ssize_t gokm_fs_write_iter(struct kiocb *iocb, struct iov_iter *from)
{
	struct inode *inode = file_inode(iocb->ki_filp);
	ssize_t ret;
	size_t count;
	void *buf = NULL;
	s64 n;

	inode_lock(inode);
	ret = generic_write_checks(iocb, from);	/* O_APPEND, limits */
	if (ret <= 0)
		goto out;
	count = min_t(size_t, ret, GOKM_FS_MAX_IO);
	buf = kvmalloc(count, GFP_KERNEL);
	if (!buf) {
		ret = -ENOMEM;
		goto out;
	}
	count = copy_from_iter(buf, count, from);
	if (!count) {
		ret = -EFAULT;
		goto out;
	}
	if (inode->i_mapping->nrpages) {
		ret = filemap_write_and_wait_range(inode->i_mapping, iocb->ki_pos,
						   iocb->ki_pos + count - 1);
		if (ret)
			goto out;
	}

	GOKM_ENTER();
	n = gokm_fs_go_write(gokm_fs_mount(inode->i_sb), inode->i_ino, buf, count,
			     iocb->ki_pos);
	GOKM_LEAVE();
	if (n < 0) {
		ret = n;
		goto out;
	}
	if (n > (s64)count)
		n = count;

	ret = gokm_fs_refresh(inode, false);	/* new size */
	if (ret)
		goto out;
	if (n > 0 && inode->i_mapping->nrpages)	/* mmaps see the new data */
		invalidate_inode_pages2_range(inode->i_mapping, iocb->ki_pos >> PAGE_SHIFT,
					      (iocb->ki_pos + n - 1) >> PAGE_SHIFT);
	inode_set_mtime_to_ts(inode, inode_set_ctime_current(inode));
	iocb->ki_pos += n;
	ret = n;
out:
	inode_unlock(inode);
	kvfree(buf);
	return ret;
}

static int gokm_fs_setattr(struct mnt_idmap *idmap, struct dentry *dentry,
			   struct iattr *attr)
{
	struct inode *inode = d_inode(dentry);
	long err;

	err = setattr_prepare(idmap, dentry, attr);
	if (err)
		return err;

	if ((attr->ia_valid & ATTR_SIZE) && S_ISREG(inode->i_mode) &&
	    attr->ia_size != i_size_read(inode)) {
		err = filemap_write_and_wait(inode->i_mapping);
		if (err)
			return err;
		GOKM_ENTER();
		err = gokm_fs_go_truncate(gokm_fs_mount(inode->i_sb), inode->i_ino,
					  attr->ia_size);
		GOKM_LEAVE();
		if (err)
			return err;
		truncate_setsize(inode, attr->ia_size);	/* + cached pages */
		inode_set_mtime_to_ts(inode, inode_set_ctime_current(inode));
	}
	if (attr->ia_valid & (ATTR_ATIME | ATTR_MTIME)) {
		GOKM_ENTER();
		err = gokm_fs_go_settimes(gokm_fs_mount(inode->i_sb), inode->i_ino,
					  (attr->ia_valid & ATTR_ATIME) ? gokm_fs_ns(attr->ia_atime) : 0,
					  (attr->ia_valid & ATTR_MTIME) ? gokm_fs_ns(attr->ia_mtime) : 0);
		GOKM_LEAVE();
		if (err)
			return err;
	}
	setattr_copy(idmap, inode, attr);
	return 0;
}

/* fsync: flush the filesystem's metadata (Syncer), then the device. */
static int gokm_fs_fsync(struct file *file, loff_t start, loff_t end, int datasync)
{
	struct super_block *sb = file_inode(file)->i_sb;
	long err;

	err = file_write_and_wait_range(file, start, end);	/* mmap writes */
	if (err)
		return err;
	GOKM_ENTER();
	err = gokm_fs_go_sync(gokm_fs_mount(sb));
	GOKM_LEAVE();
	if (!err && sb->s_bdev)
		err = sync_blockdev(sb->s_bdev);
	return err;
}

/* ---- Page cache (mmap) --------------------------------------------- */

static int gokm_fs_read_folio(struct file *file, struct folio *folio)
{
	struct inode *inode = folio->mapping->host;
	size_t len = folio_size(folio);
	void *buf;
	s64 n;

	/* Order-0 folios only (no large folio support set on the mapping). */
	buf = kmap_local_folio(folio, 0);
	GOKM_ENTER();
	n = gokm_fs_go_read(gokm_fs_mount(inode->i_sb), inode->i_ino, buf, len,
			    folio_pos(folio));
	GOKM_LEAVE();
	if (n >= 0 && n <= (s64)len)
		memset(buf + n, 0, len - n);	/* past EOF */
	else if (n >= 0)
		n = -EIO;
	kunmap_local(buf);
	folio_end_read(folio, n >= 0);
	return n < 0 ? n : 0;
}

static int gokm_fs_write_folio(struct folio *folio)
{
	struct inode *inode = folio->mapping->host;
	loff_t pos = folio_pos(folio), isize = i_size_read(inode);
	size_t len;
	void *buf;
	s64 n;

	if (pos >= isize) {	/* truncated meanwhile */
		folio_unlock(folio);
		return 0;
	}
	len = min_t(loff_t, folio_size(folio), isize - pos);
	folio_start_writeback(folio);
	folio_unlock(folio);

	buf = kmap_local_folio(folio, 0);
	GOKM_ENTER();
	n = gokm_fs_go_write(gokm_fs_mount(inode->i_sb), inode->i_ino, buf, len, pos);
	GOKM_LEAVE();
	kunmap_local(buf);
	if (n >= 0 && n < (s64)len)
		n = -EIO;
	if (n < 0)
		mapping_set_error(folio->mapping, n);
	folio_end_writeback(folio);
	return n < 0 ? n : 0;
}

static int gokm_fs_writepages(struct address_space *mapping, struct writeback_control *wbc)
{
	struct folio *folio = NULL;
	int err = 0;

	while ((folio = writeback_iter(mapping, wbc, folio, &err)))
		err = gokm_fs_write_folio(folio);
	return err;
}

static const struct address_space_operations gokm_fs_aops = {
	.read_folio	= gokm_fs_read_folio,
	.writepages	= gokm_fs_writepages,
	.dirty_folio	= filemap_dirty_folio,
};

/* ---- Extended attributes --------------------------------------------- */

static int gokm_fs_xattr_get(const struct xattr_handler *h, struct dentry *dentry,
			     struct inode *inode, const char *name, void *buffer, size_t size)
{
	const char *full = xattr_full_name(h, name);
	long n;

	/* buffer is kernel memory (the syscalls copy to user space). */
	GOKM_ENTER();
	n = gokm_fs_go_getxattr(gokm_fs_mount(inode->i_sb), inode->i_ino, full,
				strlen(full), buffer, size);
	GOKM_LEAVE();
	return n;
}

static int gokm_fs_xattr_set(const struct xattr_handler *h, struct mnt_idmap *idmap,
			     struct dentry *dentry, struct inode *inode, const char *name,
			     const void *value, size_t size, int flags)
{
	const char *full = xattr_full_name(h, name);
	long err;

	GOKM_ENTER();
	err = gokm_fs_go_setxattr(gokm_fs_mount(inode->i_sb), inode->i_ino, full,
				  strlen(full), value, size, !value, flags);
	GOKM_LEAVE();
	if (!err)
		inode_set_ctime_current(inode);
	return err;
}

static ssize_t gokm_fs_listxattr(struct dentry *dentry, char *list, size_t size)
{
	struct inode *inode = d_inode(dentry);
	long n;

	GOKM_ENTER();
	n = gokm_fs_go_listxattr(gokm_fs_mount(inode->i_sb), inode->i_ino, list, size);
	GOKM_LEAVE();
	return n;
}

static const struct xattr_handler gokm_fs_xattr_user = {
	.prefix	= XATTR_USER_PREFIX,
	.get	= gokm_fs_xattr_get,
	.set	= gokm_fs_xattr_set,
};

static const struct xattr_handler gokm_fs_xattr_trusted = {
	.prefix	= XATTR_TRUSTED_PREFIX,
	.get	= gokm_fs_xattr_get,
	.set	= gokm_fs_xattr_set,
};

static const struct xattr_handler * const gokm_fs_xattr_handlers[] = {
	&gokm_fs_xattr_user,
	&gokm_fs_xattr_trusted,
	NULL,
};

static const struct inode_operations gokm_fs_dir_iops = {
	.lookup		= gokm_fs_lookup,
	.create		= gokm_fs_create,
	.mkdir		= gokm_fs_mkdir,
	.unlink		= gokm_fs_unlink,
	.rmdir		= gokm_fs_rmdir,
	.rename		= gokm_fs_rename,
	.symlink	= gokm_fs_symlink,
	.setattr	= gokm_fs_setattr,
	.getattr	= simple_getattr,
	.listxattr	= gokm_fs_listxattr,
};

static const struct inode_operations gokm_fs_file_iops = {
	.setattr	= gokm_fs_setattr,
	.getattr	= simple_getattr,
	.listxattr	= gokm_fs_listxattr,
};

static const struct inode_operations gokm_fs_symlink_iops = {
	.get_link	= gokm_fs_get_link,
	.getattr	= simple_getattr,
	.listxattr	= gokm_fs_listxattr,
};

static const struct file_operations gokm_fs_dir_fops = {
	.llseek		= generic_file_llseek,
	.read		= generic_read_dir,
	.iterate_shared	= gokm_fs_iterate,
};

static const struct file_operations gokm_fs_file_fops = {
	.llseek		= generic_file_llseek,
	.read_iter	= gokm_fs_read_iter,
	.write_iter	= gokm_fs_write_iter,
	.fsync		= gokm_fs_fsync,
	.mmap_prepare	= generic_file_mmap_prepare,
};

/* ---- Superblock ------------------------------------------------------ */

static void gokm_fs_evict_inode(struct inode *inode)
{
	truncate_inode_pages_final(&inode->i_data);
	clear_inode(inode);
	if (!inode->i_nlink) {
		GOKM_ENTER();
		gokm_fs_go_evict(gokm_fs_mount(inode->i_sb), inode->i_ino);
		GOKM_LEAVE();
	}
}

static int gokm_fs_statfs(struct dentry *dentry, struct kstatfs *buf)
{
	struct super_block *sb = dentry->d_sb;
	s64 bsize, blocks, bfree, bavail, files, ffree, namelen;
	long err;

	GOKM_ENTER();
	err = gokm_fs_go_statfs(gokm_fs_mount(sb), &bsize, &blocks, &bfree, &bavail,
				&files, &ffree, &namelen);
	GOKM_LEAVE();
	if (err == -ENOSYS)
		return simple_statfs(dentry, buf);
	if (err)
		return err;
	buf->f_type = sb->s_magic;
	buf->f_bsize = bsize;
	buf->f_blocks = blocks;
	buf->f_bfree = bfree;
	buf->f_bavail = bavail;
	buf->f_files = files;
	buf->f_ffree = ffree;
	buf->f_namelen = namelen;
	if (sb->s_bdev)
		buf->f_fsid = u64_to_fsid(huge_encode_dev(sb->s_bdev->bd_dev));
	return 0;
}

static int gokm_fs_sync_fs(struct super_block *sb, int wait)
{
	long err;

	GOKM_ENTER();
	err = gokm_fs_go_sync(gokm_fs_mount(sb));
	GOKM_LEAVE();
	return err;
}

/* Before the device is released: the filesystem flushes its state. */
static void gokm_fs_put_super(struct super_block *sb)
{
	GOKM_ENTER();
	gokm_fs_go_unmount(gokm_fs_mount(sb));
	GOKM_LEAVE();
}

static const struct super_operations gokm_fs_sops = {
	.statfs		= gokm_fs_statfs,
	.evict_inode	= gokm_fs_evict_inode,
	.sync_fs	= gokm_fs_sync_fs,
	.put_super	= gokm_fs_put_super,
};

/* ---- Block devices (fs.Device) ---------------------------------------- */

long gokm_fs_dev_read(unsigned long sbp, u64 block, void *dst, long nblocks)
{
	struct super_block *sb = (struct super_block *)sbp;
	long i, err = 0;

	/*
	 * May wait for I/O: let other tasks run Go (and collect) meanwhile. dst
	 * stays valid: it's referenced from the parked caller and the
	 * collector doesn't move objects.
	 */
	GOKM_PARK();
	for (i = 0; i < nblocks; i++) {
		struct buffer_head *bh = sb_bread(sb, block + i);

		if (!bh) {
			err = -EIO;
			break;
		}
		memcpy(dst + i * sb->s_blocksize, bh->b_data, sb->s_blocksize);
		brelse(bh);
	}
	GOKM_UNPARK();
	return err;
}

long gokm_fs_dev_write(unsigned long sbp, u64 block, const void *src, long nblocks)
{
	struct super_block *sb = (struct super_block *)sbp;
	long i, err = 0;

	GOKM_PARK();
	for (i = 0; i < nblocks; i++) {
		struct buffer_head *bh = sb_getblk(sb, block + i);

		if (!bh) {
			err = -ENOMEM;
			break;
		}
		lock_buffer(bh);
		memcpy(bh->b_data, src + i * sb->s_blocksize, sb->s_blocksize);
		set_buffer_uptodate(bh);
		unlock_buffer(bh);
		mark_buffer_dirty(bh);
		brelse(bh);
	}
	GOKM_UNPARK();
	return err;
}

long gokm_fs_dev_sync(unsigned long sbp)
{
	struct super_block *sb = (struct super_block *)sbp;
	long err;

	GOKM_PARK();
	err = sync_blockdev(sb->s_bdev);
	GOKM_UNPARK();
	return err;
}

long gokm_fs_dev_set_blocksize(unsigned long sbp, long size)
{
	return sb_set_blocksize((struct super_block *)sbp, size) ? 0 : -EINVAL;
}

static int gokm_fs_fill_super(struct super_block *sb, struct fs_context *fc)
{
	struct gokm_fs_type *t = container_of(fc->fs_type, struct gokm_fs_type, type);
	unsigned long m = 0;
	struct inode *root;
	long err;

	s64 dev_size = 0;

	sb->s_maxbytes = t->max_bytes ? t->max_bytes : MAX_LFS_FILESIZE;
	sb->s_magic = t->magic;
	sb->s_op = &gokm_fs_sops;
	sb->s_xattr = gokm_fs_xattr_handlers;
	sb->s_time_gran = 1;
	if (t->requires_dev) {
		if (!sb_set_blocksize(sb, max_t(int, 512, bdev_logical_block_size(sb->s_bdev))))
			return -EINVAL;
		dev_size = bdev_nr_bytes(sb->s_bdev);
	} else {
		sb->s_blocksize = PAGE_SIZE;
		sb->s_blocksize_bits = PAGE_SHIFT;
		/* Without a device the sb gets noop_backing_dev_info, which
		 * never writes back: dirty mmap pages wouldn't reach Go. */
		err = super_setup_bdi(sb);
		if (err)
			return err;
	}

	GOKM_ENTER();
	err = gokm_fs_go_mount(t->handle, (unsigned long)sb, sb->s_blocksize, dev_size,
			       sb_rdonly(sb), &m);
	GOKM_LEAVE();
	if (err)
		return err;
	sb->s_fs_info = (void *)m;	/* released in gokm_fs_kill_sb */

	root = gokm_fs_iget(sb, GOKM_FS_ROOT_INO);
	if (IS_ERR(root))
		return PTR_ERR(root);
	sb->s_root = d_make_root(root);
	return sb->s_root ? 0 : -ENOMEM;
}

static int gokm_fs_get_tree(struct fs_context *fc)
{
	struct gokm_fs_type *t = container_of(fc->fs_type, struct gokm_fs_type, type);

	if (t->requires_dev)
		return get_tree_bdev(fc, gokm_fs_fill_super);
	return get_tree_nodev(fc, gokm_fs_fill_super);
}

static const struct fs_context_operations gokm_fs_context_ops = {
	.get_tree	= gokm_fs_get_tree,
};

static int gokm_fs_init_fs_context(struct fs_context *fc)
{
	fc->ops = &gokm_fs_context_ops;
	return 0;
}

static void gokm_fs_kill_sb(struct super_block *sb)
{
	struct gokm_fs_type *t = container_of(sb->s_type, struct gokm_fs_type, type);
	unsigned long m = gokm_fs_mount(sb);

	/* Evicts inodes and calls put_super (both may call into Go). */
	if (t->requires_dev)
		kill_block_super(sb);
	else
		kill_anon_super(sb);
	if (m) {
		GOKM_ENTER();
		gokm_fs_go_release(m);
		GOKM_LEAVE();
	}
}

long gokm_fs_register(const char *name, long len, u32 magic, bool requires_dev,
		      s64 max_bytes, unsigned long handle, unsigned long *out)
{
	struct gokm_fs_type *t;
	char *kname;
	int err;

	t = kzalloc(sizeof(*t), GFP_KERNEL);
	kname = kstrndup(name, len, GFP_KERNEL);
	if (!t || !kname) {
		kfree(t);
		kfree(kname);
		return -ENOMEM;
	}
	t->type.owner = THIS_MODULE;
	t->type.name = kname;
	t->type.init_fs_context = gokm_fs_init_fs_context;
	t->type.kill_sb = gokm_fs_kill_sb;
	t->handle = handle;
	t->magic = magic;
	t->requires_dev = requires_dev;
	t->max_bytes = max_bytes;
	if (requires_dev)
		t->type.fs_flags = FS_REQUIRES_DEV;

	err = register_filesystem(&t->type);
	if (err) {
		kfree(kname);
		kfree(t);
		return err;
	}
	*out = (unsigned long)t;
	return 0;
}

void gokm_fs_unregister(unsigned long ptr)
{
	struct gokm_fs_type *t = (struct gokm_fs_type *)ptr;

	GOKM_PARK();
	unregister_filesystem(&t->type);
	GOKM_UNPARK();
	kfree(t->type.name);
	kfree(t);
}
