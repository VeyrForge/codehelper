package com.example.demo;

import org.springframework.stereotype.Service;

@Service
public class PetService {
  public String greet(String name) {
    return "hello " + name;
  }
}
