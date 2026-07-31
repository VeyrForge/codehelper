package com.example.demo

import org.springframework.stereotype.Service

@Service
class PetService {
    fun greet(name: String): String {
        return "hello " + name
    }
}
