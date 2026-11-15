// SPDX-License-Identifier: GPL-2.0
/*
 * gokm/blk glue: block devices whose storage is Go (lib/blk/blk.go).
 *
 * A blk-mq queue in blocking mode (BLK_MQ_F_BLOCKING): ->queue_rq runs in
 * process context (the block layer's workers or the submitter), so it can
 * enter Go, which may sleep and allocate. Each request's segments are
 * mapped and handed to Go one by one. The disk gets a dynamic device
 * number (no registered major), with room for partitions.
 */
#include <linux/module.h>
#include <linux/blkdev.h>
#include <linux/blk-mq.h>
#include <linux/bvec.h>
#include <linux/highmem.h>
#include <linux/slab.h>
#include <linux/string.h>
#include <linux/atomic.h>

#include "gokm.h"

/* Implemented in Go. */
long gokm_blk_go_rw(unsigned long h, bool write, void *buf, long n, s64 off);
long gokm_blk_go_flush(unsigned long h);
long gokm_blk_go_discard(unsigned long h, s64 off, s64 n);

/* Called from Go. */
long gokm_blk_create(const char *name, s64 size, long lbs, long pbs, long max_io, u32 flags,
		     unsigned long handle, unsigned long *out, char *out_name);
void gokm_blk_destroy(unsigned long ptr);

#define GOKM_BLK_RO		(1 << 0)
#define GOKM_BLK_ROTATIONAL	(1 << 1)
#define GOKM_BLK_FLUSH		(1 << 2)
#define GOKM_BLK_DISCARD	(1 << 3)

struct gokm_blk {
	struct blk_mq_tag_set tag_set;
	struct gendisk *disk;
	unsigned long handle;	/* kernel.Handle of the Go Disk */
	bool flush;
};

static atomic_t gokm_blk_index = ATOMIC_INIT(0);

static long gokm_blk_flush(struct gokm_blk *b)
{
	long err;

	GOKM_ENTER();
	err = gokm_blk_go_flush(b->handle);
	GOKM_LEAVE();
	return err;
}

static blk_status_t gokm_blk_queue_rq(struct blk_mq_hw_ctx *hctx,
				      const struct blk_mq_queue_data *bd)
{
	struct request *rq = bd->rq;
	struct gokm_blk *b = hctx->queue->queuedata;
	loff_t pos = blk_rq_pos(rq) << SECTOR_SHIFT;
	long err = 0;

	blk_mq_start_request(rq);
	switch (req_op(rq)) {
	case REQ_OP_READ:
	case REQ_OP_WRITE: {
		bool write = req_op(rq) == REQ_OP_WRITE;
		struct req_iterator iter;
		struct bio_vec bvec;

		if (write && (rq->cmd_flags & REQ_PREFLUSH) && b->flush)
			err = gokm_blk_flush(b);
		if (!err) {
			GOKM_ENTER();
			rq_for_each_segment(bvec, rq, iter) {
				void *p = bvec_kmap_local(&bvec);

				err = gokm_blk_go_rw(b->handle, write, p, bvec.bv_len, pos);
				kunmap_local(p);
				if (err)
					break;
				pos += bvec.bv_len;
			}
			GOKM_LEAVE();
		}
		if (!err && write && (rq->cmd_flags & REQ_FUA) && b->flush)
			err = gokm_blk_flush(b);
		break;
	}
	case REQ_OP_FLUSH:
		err = b->flush ? gokm_blk_flush(b) : 0;
		break;
	case REQ_OP_DISCARD: {
		GOKM_ENTER();
		err = gokm_blk_go_discard(b->handle, pos, blk_rq_bytes(rq));
		GOKM_LEAVE();
		break;
	}
	default:
		err = -EOPNOTSUPP;
	}
	blk_mq_end_request(rq, errno_to_blk_status(err));
	return BLK_STS_OK;
}

static const struct blk_mq_ops gokm_blk_mq_ops = {
	.queue_rq	= gokm_blk_queue_rq,
};

static const struct block_device_operations gokm_blk_fops = {
	.owner		= THIS_MODULE,
};

long gokm_blk_create(const char *name, s64 size, long lbs, long pbs, long max_io, u32 flags,
		     unsigned long handle, unsigned long *out, char *out_name)
{
	struct queue_limits lim = {
		.logical_block_size	= lbs,
		.physical_block_size	= pbs,
		.max_hw_sectors		= max_io >> SECTOR_SHIFT,
	};
	struct gokm_blk *b;
	struct gendisk *disk;
	int err;

	if (flags & GOKM_BLK_FLUSH)
		lim.features |= BLK_FEAT_WRITE_CACHE | BLK_FEAT_FUA;
	if (flags & GOKM_BLK_ROTATIONAL)
		lim.features |= BLK_FEAT_ROTATIONAL;
	if (flags & GOKM_BLK_DISCARD) {
		lim.max_hw_discard_sectors = UINT_MAX;
		lim.discard_granularity = pbs;
	}

	b = kzalloc(sizeof(*b), GFP_KERNEL);
	if (!b)
		return -ENOMEM;
	b->handle = handle;
	b->flush = flags & GOKM_BLK_FLUSH;

	b->tag_set.ops = &gokm_blk_mq_ops;
	b->tag_set.nr_hw_queues = 1;
	b->tag_set.queue_depth = 128;
	b->tag_set.numa_node = NUMA_NO_NODE;
	b->tag_set.flags = BLK_MQ_F_BLOCKING;
	err = blk_mq_alloc_tag_set(&b->tag_set);
	if (err)
		goto free;

	disk = blk_mq_alloc_disk(&b->tag_set, &lim, b);
	if (IS_ERR(disk)) {
		err = PTR_ERR(disk);
		goto free_tags;
	}
	b->disk = disk;
	disk->fops = &gokm_blk_fops;
	disk->private_data = b;
	if (strstr(name, "%d"))
		snprintf(disk->disk_name, DISK_NAME_LEN, name, atomic_inc_return(&gokm_blk_index) - 1);
	else
		strscpy(disk->disk_name, name, DISK_NAME_LEN);
	set_capacity(disk, size >> SECTOR_SHIFT);
	if (flags & GOKM_BLK_RO)
		set_disk_ro(disk, true);

	{
		/* Scans partitions, i.e. reads through Go from other tasks. */
		GOKM_PARK();
		err = add_disk(disk);
		GOKM_UNPARK();
	}
	if (err)
		goto put;
	strscpy(out_name, disk->disk_name, 32);
	*out = (unsigned long)b;
	return 0;

put:
	put_disk(disk);
free_tags:
	blk_mq_free_tag_set(&b->tag_set);
free:
	kfree(b);
	return err;
}

void gokm_blk_destroy(unsigned long ptr)
{
	struct gokm_blk *b = (struct gokm_blk *)ptr;

	/* Waits for in-flight I/O, which needs to enter Go. */
	GOKM_PARK();
	del_gendisk(b->disk);
	put_disk(b->disk);
	blk_mq_free_tag_set(&b->tag_set);
	GOKM_UNPARK();
	kfree(b);
}
