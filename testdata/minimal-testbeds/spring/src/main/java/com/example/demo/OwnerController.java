package com.example.demo;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class OwnerController {
  private final PetService pets;

  public OwnerController(PetService pets) {
    this.pets = pets;
  }

  @GetMapping("/owners/{name}")
  public String greet(@PathVariable String name) {
    return this.pets.greet(name);
  }
}
