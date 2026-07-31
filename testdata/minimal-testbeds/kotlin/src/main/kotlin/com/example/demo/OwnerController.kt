package com.example.demo

import org.springframework.web.bind.annotation.GetMapping
import org.springframework.web.bind.annotation.PathVariable
import org.springframework.web.bind.annotation.RestController

@RestController
class OwnerController(
    private val pets: PetService
) {
    @GetMapping("/owners/{name}")
    fun greet(@PathVariable name: String): String {
        return pets.greet(name)
    }
}
