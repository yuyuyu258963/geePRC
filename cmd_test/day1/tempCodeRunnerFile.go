
		var reply string
		_ = cc.ReadBody(&reply)
		log.Println("reply:", reply)