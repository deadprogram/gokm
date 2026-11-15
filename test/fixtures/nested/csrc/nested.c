// SPDX-License-Identifier: GPL-2.0
#include <linux/types.h>
#include "gokm.h"

int nested_go_callback(int depth); /* Go */
int nested_call_back(int depth);   /* called from Go */

int nested_call_back(int depth)
{
	int ret;

	GOKM_ENTER();
	ret = nested_go_callback(depth);
	GOKM_LEAVE();
	return ret;
}
