// SPDX-License-Identifier: GPL-2.0
/*
 * gokm/netdev glue: network devices driven by Go (lib/netdev/netdev.go):
 * point-to-point IP devices (like TUN) or Ethernet devices (like TAP: whole
 * frames, ARP and neighbor discovery done by the kernel).
 *
 * ndo_start_xmit runs with BH disabled (atomic context). By default it
 * queues the packet and a work item hands queued packets to Go in process
 * context. With AtomicTransmit, Go's Transmit runs right there instead,
 * through an atomic try-entry; when that can't enter (a collection is in
 * progress) or packets are already queued (keep the order), it falls back
 * to the queue. Locking: ndo_open/ndo_stop run under rtnl and then enter Go, so
 * Go must never wait for rtnl while holding the Go lock: register/unregister
 * park (release the Go lock) first.
 */
#include <linux/module.h>
#include <linux/netdevice.h>
#include <linux/skbuff.h>
#include <linux/if_arp.h>
#include <linux/if_ether.h>
#include <linux/etherdevice.h>
#include <linux/if_vlan.h>
#include <linux/rtnetlink.h>
#include <linux/workqueue.h>
#include <linux/string.h>

#include "gokm.h"

#define GOKM_NETDEV_TXQ_MAX	1000

/* Implemented in Go. */
void gokm_netdev_go_xmit(unsigned long h, const void *pkt, long len);
long gokm_netdev_go_open(unsigned long h);
long gokm_netdev_go_stop(unsigned long h);

/* Called from Go. */
long gokm_netdev_create(const char *name, long mtu, bool atomic_xmit, bool ether,
			const u8 *mac, unsigned long handle, unsigned long *out, char *out_name);
void gokm_netdev_mac(unsigned long ptr, u8 *mac);
void gokm_netdev_mac(unsigned long ptr, u8 *mac)
{
	struct net_device *dev = (struct net_device *)ptr;

	u8 addr[ETH_ALEN];

	/* dev_addr changes under rtnl (ip link set address). */
	{
		GOKM_PARK();
		rtnl_lock();
		ether_addr_copy(addr, dev->dev_addr);
		rtnl_unlock();
		GOKM_UNPARK();
	}
	memcpy(mac, addr, ETH_ALEN);	/* Go memory: after unparking */
}

void gokm_netdev_stats(unsigned long ptr, u64 *direct, u64 *deferred);
void gokm_netdev_destroy(unsigned long ptr);
long gokm_netdev_rx(unsigned long ptr, const void *pkt, long len);

struct gokm_netdev_priv {
	struct net_device *dev;
	unsigned long handle;	/* kernel.Handle of the Go Interface */
	struct sk_buff_head txq;
	struct work_struct tx_work;
	bool atomic_xmit;
	bool ether;
	atomic64_t direct;	/* packets Go handled in the transmit path */
	atomic64_t deferred;	/* packets handed to Go from the workqueue */
};

static netdev_tx_t gokm_netdev_xmit(struct sk_buff *skb, struct net_device *dev)
{
	struct gokm_netdev_priv *p = netdev_priv(dev);

	if (p->atomic_xmit && skb_queue_empty(&p->txq) && !skb_linearize(skb)) {
		GOKM_DECLARE_ENTRY();

		if (GOKM_TRY_ENTER_ATOMIC()) {
			DEV_STATS_INC(dev, tx_packets);
			DEV_STATS_ADD(dev, tx_bytes, skb->len);
			gokm_netdev_go_xmit(p->handle, skb->data, skb->len);
			GOKM_LEAVE();
			atomic64_inc(&p->direct);
			consume_skb(skb);
			return NETDEV_TX_OK;
		}
	}

	if (skb_queue_len(&p->txq) >= GOKM_NETDEV_TXQ_MAX) {
		DEV_STATS_INC(dev, tx_dropped);
		dev_kfree_skb_any(skb);
		return NETDEV_TX_OK;
	}
	skb_queue_tail(&p->txq, skb);
	schedule_work(&p->tx_work);
	return NETDEV_TX_OK;
}

static void gokm_netdev_tx_work(struct work_struct *work)
{
	struct gokm_netdev_priv *p = container_of(work, struct gokm_netdev_priv, tx_work);
	struct sk_buff *skb;

	while ((skb = skb_dequeue(&p->txq))) {
		if (skb_linearize(skb)) {
			DEV_STATS_INC(p->dev, tx_dropped);
			kfree_skb(skb);
			continue;
		}
		DEV_STATS_INC(p->dev, tx_packets);
		DEV_STATS_ADD(p->dev, tx_bytes, skb->len);
		atomic64_inc(&p->deferred);

		GOKM_ENTER();
		gokm_netdev_go_xmit(p->handle, skb->data, skb->len);
		GOKM_LEAVE();
		consume_skb(skb);
	}
}

static int gokm_netdev_open(struct net_device *dev)
{
	struct gokm_netdev_priv *p = netdev_priv(dev);
	long err;

	GOKM_ENTER();
	err = gokm_netdev_go_open(p->handle);
	GOKM_LEAVE();
	if (!err)
		netif_start_queue(dev);
	return err;
}

static int gokm_netdev_stop(struct net_device *dev)
{
	struct gokm_netdev_priv *p = netdev_priv(dev);
	long err;

	netif_stop_queue(dev);
	GOKM_ENTER();
	err = gokm_netdev_go_stop(p->handle);
	GOKM_LEAVE();
	return err;
}

static const struct net_device_ops gokm_netdev_ops = {
	.ndo_open	= gokm_netdev_open,
	.ndo_stop	= gokm_netdev_stop,
	.ndo_start_xmit	= gokm_netdev_xmit,
};

static const struct net_device_ops gokm_netdev_ether_ops = {
	.ndo_open		= gokm_netdev_open,
	.ndo_stop		= gokm_netdev_stop,
	.ndo_start_xmit		= gokm_netdev_xmit,
	.ndo_set_mac_address	= eth_mac_addr,
	.ndo_validate_addr	= eth_validate_addr,
};

static void gokm_netdev_ether_setup(struct net_device *dev)
{
	ether_setup(dev);
	dev->netdev_ops = &gokm_netdev_ether_ops;
	dev->min_mtu = ETH_MIN_MTU;
	dev->max_mtu = ETH_MAX_MTU;
	dev->priv_flags |= IFF_NO_QUEUE | IFF_LIVE_ADDR_CHANGE;
	dev->tx_queue_len = 0;
}

static void gokm_netdev_setup(struct net_device *dev)
{
	dev->netdev_ops = &gokm_netdev_ops;
	dev->type = ARPHRD_NONE;
	dev->hard_header_len = 0;
	dev->addr_len = 0;
	dev->mtu = 1500;
	dev->min_mtu = 68;
	dev->max_mtu = 65535;
	dev->flags = IFF_POINTOPOINT | IFF_NOARP | IFF_MULTICAST;
	dev->tx_queue_len = 0;
	dev->priv_flags |= IFF_NO_QUEUE;
}

long gokm_netdev_create(const char *name, long mtu, bool atomic_xmit, bool ether,
			const u8 *mac, unsigned long handle, unsigned long *out, char *out_name)
{
	struct gokm_netdev_priv *p;
	struct net_device *dev;
	int err;

	dev = alloc_netdev(sizeof(*p), name, NET_NAME_USER,
			   ether ? gokm_netdev_ether_setup : gokm_netdev_setup);
	if (!dev)
		return -ENOMEM;
	dev->mtu = mtu;
	if (ether) {
		if (is_zero_ether_addr(mac)) {
			eth_hw_addr_random(dev);
		} else if (is_valid_ether_addr(mac)) {
			eth_hw_addr_set(dev, mac);
		} else {
			free_netdev(dev);
			return -EADDRNOTAVAIL;
		}
	}
	p = netdev_priv(dev);
	p->ether = ether;
	p->dev = dev;
	p->handle = handle;
	p->atomic_xmit = atomic_xmit;
	atomic64_set(&p->direct, 0);
	atomic64_set(&p->deferred, 0);
	skb_queue_head_init(&p->txq);
	INIT_WORK(&p->tx_work, gokm_netdev_tx_work);

	GOKM_PARK();
	err = register_netdev(dev);	/* takes rtnl */
	GOKM_UNPARK();
	if (err) {
		free_netdev(dev);
		return err;
	}
	strscpy(out_name, dev->name, IFNAMSIZ);
	*out = (unsigned long)dev;
	return 0;
}

void gokm_netdev_destroy(unsigned long ptr)
{
	struct net_device *dev = (struct net_device *)ptr;
	struct gokm_netdev_priv *p = netdev_priv(dev);

	GOKM_PARK();
	unregister_netdev(dev);		/* may call ndo_stop -> Go */
	cancel_work_sync(&p->tx_work);	/* may be waiting to enter Go */
	skb_queue_purge(&p->txq);
	free_netdev(dev);
	GOKM_UNPARK();
}

long gokm_netdev_rx(unsigned long ptr, const void *pkt, long len)
{
	struct net_device *dev = (struct net_device *)ptr;
	struct gokm_netdev_priv *p = netdev_priv(dev);
	struct sk_buff *skb;
	u8 version;

	if (p->ether) {
		if (len < ETH_HLEN || len > dev->mtu + ETH_HLEN + VLAN_HLEN)
			return -EINVAL;
		skb = netdev_alloc_skb_ip_align(dev, len);
		if (!skb) {
			DEV_STATS_INC(dev, rx_dropped);
			return -ENOMEM;
		}
		skb_put_data(skb, pkt, len);
		skb->protocol = eth_type_trans(skb, dev);
		DEV_STATS_INC(dev, rx_packets);
		DEV_STATS_ADD(dev, rx_bytes, len);
		netif_rx(skb);
		return 0;
	}

	if (len <= 0 || len > dev->mtu)
		return -EINVAL;
	version = *(const u8 *)pkt >> 4;
	if (version != 4 && version != 6)
		return -EINVAL;

	skb = netdev_alloc_skb(dev, len);
	if (!skb) {
		DEV_STATS_INC(dev, rx_dropped);
		return -ENOMEM;
	}
	skb_put_data(skb, pkt, len);
	skb_reset_network_header(skb);
	skb->protocol = htons(version == 4 ? ETH_P_IP : ETH_P_IPV6);
	skb->pkt_type = PACKET_HOST;
	DEV_STATS_INC(dev, rx_packets);
	DEV_STATS_ADD(dev, rx_bytes, len);
	netif_rx(skb);
	return 0;
}

void gokm_netdev_stats(unsigned long ptr, u64 *direct, u64 *deferred)
{
	struct gokm_netdev_priv *p = netdev_priv((struct net_device *)ptr);

	*direct = atomic64_read(&p->direct);
	*deferred = atomic64_read(&p->deferred);
}
