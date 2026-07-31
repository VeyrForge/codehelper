package com.example.demo;

import jakarta.persistence.Entity;
import jakarta.persistence.Id;
import jakarta.persistence.ManyToOne;

@Entity
public class Pet {
  @Id
  private Long id;

  @ManyToOne
  private Owner owner;

  public Long getId() { return id; }
}
