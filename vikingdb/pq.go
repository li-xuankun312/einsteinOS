package vikingdb

type pqItem struct {
	id   uint64
	dist float32
}

type minPQ struct {
	items []pqItem
}

func newMinPQ(cap int) *minPQ {
	return &minPQ{items: make([]pqItem, 0, cap)}
}

func (q *minPQ) Len() int { return len(q.items) }

func (q *minPQ) Push(id uint64, dist float32) {
	q.items = append(q.items, pqItem{id, dist})
	q.siftUp(len(q.items) - 1)
}

func (q *minPQ) Pop() pqItem {
	top := q.items[0]
	n := len(q.items) - 1
	q.items[0] = q.items[n]
	q.items = q.items[:n]
	if n > 0 {
		q.siftDown(0)
	}
	return top
}

func (q *minPQ) Top() pqItem {
	return q.items[0]
}

func (q *minPQ) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if q.items[i].dist < q.items[parent].dist {
			q.items[i], q.items[parent] = q.items[parent], q.items[i]
			i = parent
		} else {
			break
		}
	}
}

func (q *minPQ) siftDown(i int) {
	n := len(q.items)
	for {
		left := 2*i + 1
		right := 2*i + 2
		smallest := i
		if left < n && q.items[left].dist < q.items[smallest].dist {
			smallest = left
		}
		if right < n && q.items[right].dist < q.items[smallest].dist {
			smallest = right
		}
		if smallest == i {
			break
		}
		q.items[i], q.items[smallest] = q.items[smallest], q.items[i]
		i = smallest
	}
}

type maxPQ struct {
	items []pqItem
}

func newMaxPQ(cap int) *maxPQ {
	return &maxPQ{items: make([]pqItem, 0, cap)}
}

func (q *maxPQ) Len() int { return len(q.items) }

func (q *maxPQ) Push(id uint64, dist float32) {
	q.items = append(q.items, pqItem{id, dist})
	q.siftUp(len(q.items) - 1)
}

func (q *maxPQ) Pop() pqItem {
	top := q.items[0]
	n := len(q.items) - 1
	q.items[0] = q.items[n]
	q.items = q.items[:n]
	if n > 0 {
		q.siftDown(0)
	}
	return top
}

func (q *maxPQ) Top() pqItem {
	return q.items[0]
}

func (q *maxPQ) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if q.items[i].dist > q.items[parent].dist {
			q.items[i], q.items[parent] = q.items[parent], q.items[i]
			i = parent
		} else {
			break
		}
	}
}

func (q *maxPQ) siftDown(i int) {
	n := len(q.items)
	for {
		left := 2*i + 1
		right := 2*i + 2
		largest := i
		if left < n && q.items[left].dist > q.items[largest].dist {
			largest = left
		}
		if right < n && q.items[right].dist > q.items[largest].dist {
			largest = right
		}
		if largest == i {
			break
		}
		q.items[i], q.items[largest] = q.items[largest], q.items[i]
		i = largest
	}
}
