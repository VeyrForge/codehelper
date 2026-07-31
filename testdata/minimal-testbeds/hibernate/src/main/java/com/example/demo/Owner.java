package com.example.demo;

import jakarta.persistence.Entity;
import jakarta.persistence.Id;
import jakarta.persistence.ManyToOne;
import jakarta.persistence.OneToMany;
import java.util.List;

@Entity
public class Owner {
  @Id
  private Long id;

  @OneToMany(mappedBy = "owner")
  private List<Pet> pets;

  @ManyToOne
  private Account account;

  public Long getId() { return id; }
}
