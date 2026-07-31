package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseJava_JPAEntityRepositoryQuery(t *testing.T) {
	src := []byte(`
package com.example.demo;

import jakarta.persistence.Entity;
import jakarta.persistence.Id;
import jakarta.persistence.ManyToOne;
import jakarta.persistence.OneToMany;
import jakarta.persistence.EntityManager;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.data.jpa.repository.Query;
import org.springframework.data.repository.query.Param;
import org.springframework.stereotype.Repository;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;
import java.util.List;

@Entity
class Owner {
  @Id
  private Long id;

  @OneToMany(mappedBy = "owner")
  private List<Pet> pets;

  @ManyToOne
  private Account account;
}

@Entity
class Pet {
  @Id
  private Long id;
}

@Entity
class Account {
  @Id
  private Long id;
}

@Repository
interface OwnerRepository extends JpaRepository<Owner, Long> {
  @Query("SELECT o FROM Owner o WHERE o.name = :name")
  Owner findByName(@Param("name") String name);
}

@RestController
class OwnerController {
  private final OwnerRepository owners;
  private final EntityManager em;

  public OwnerController(OwnerRepository owners, EntityManager em) {
    this.owners = owners;
    this.em = em;
  }

  @GetMapping("/owners/{id}")
  public Owner get(Long id) {
    return this.em.find(Owner.class, id);
  }
}
`)
	res, err := ParseJava(context.Background(), "j", "src/main/java/com/example/demo/Owner.java", src)
	if err != nil {
		t.Fatal(err)
	}

	syms := map[string]*types.Symbol{}
	for i := range res.Symbols {
		s := &res.Symbols[i]
		syms[s.Name] = s
	}
	owner := syms["Owner"]
	if owner == nil {
		names := make([]string, 0, len(syms))
		for k := range syms {
			names = append(names, k)
		}
		t.Fatalf("missing Owner; got %v", names)
	}
	if !strings.Contains(owner.Signature, "frameworks=jpa") {
		t.Errorf("Owner missing jpa: %q", owner.Signature)
	}
	if !strings.Contains(owner.Signature, "role=entity") {
		t.Errorf("Owner missing entity role: %q", owner.Signature)
	}
	repo := syms["OwnerRepository"]
	if repo == nil {
		t.Fatalf("missing OwnerRepository")
	}
	if !strings.Contains(repo.Signature, "role=repository") {
		t.Errorf("OwnerRepository role: %q", repo.Signature)
	}
	findByName := syms["findByName"]
	if findByName == nil || !strings.Contains(findByName.Signature, "role=query") {
		t.Errorf("findByName signature=%v", findByName)
	}
	get := syms["get"]
	if get == nil || !strings.Contains(get.Signature, "role=entrypoint") {
		t.Errorf("get signature=%v want entrypoint", get)
	}

	callsFrom := map[string][]string{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		leaf := e.TargetID
		if i := strings.LastIndex(leaf, ":"); i >= 0 {
			leaf = leaf[i+1:]
		}
		callsFrom[e.SourceID] = append(callsFrom[e.SourceID], leaf)
	}
	ownerCalls := strings.Join(callsFrom[owner.ID], ",")
	for _, want := range []string{"Pet", "Account"} {
		if !strings.Contains(ownerCalls, want) {
			t.Errorf("Owner missing relation to %q in %v", want, callsFrom[owner.ID])
		}
	}
	repoCalls := strings.Join(callsFrom[repo.ID], ",")
	if !strings.Contains(repoCalls, "Owner") {
		t.Errorf("OwnerRepository missing JpaRepository entity edge; got %v", callsFrom[repo.ID])
	}
	if findByName != nil {
		fnCalls := strings.Join(callsFrom[findByName.ID], ",")
		if !strings.Contains(fnCalls, "Owner") {
			t.Errorf("findByName @Query missing Owner; got %v", callsFrom[findByName.ID])
		}
	}
	if get != nil {
		getCalls := strings.Join(callsFrom[get.ID], ",")
		if !strings.Contains(getCalls, "Owner") {
			t.Errorf("em.find(Owner.class) missing Owner edge; got %v", callsFrom[get.ID])
		}
	}
}

func TestDetectFrameworkPacks_JPA(t *testing.T) {
	t.Parallel()
	got := DetectFrameworkPacks("src/main/java/demo/Owner.java", nil, `
package demo;
import jakarta.persistence.Entity;
@Entity
class Owner {}
`)
	found := false
	for _, g := range got {
		if g == "jpa" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected jpa, got %v", got)
	}
}
