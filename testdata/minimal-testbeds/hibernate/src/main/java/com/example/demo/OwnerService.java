package com.example.demo;

import jakarta.persistence.EntityManager;
import org.springframework.stereotype.Service;

@Service
public class OwnerService {
  private final OwnerRepository owners;
  private final EntityManager em;

  public OwnerService(OwnerRepository owners, EntityManager em) {
    this.owners = owners;
    this.em = em;
  }

  public Owner find(Long id) {
    Owner cached = this.owners.findById(id).orElse(null);
    if (cached != null) {
      return cached;
    }
    return this.em.find(Owner.class, id);
  }

  public Owner byName(String name) {
    return this.owners.findByName(name);
  }
}
